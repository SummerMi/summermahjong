package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Client 是 LLM 客户端抽象：只要求「给 prompt，返回文本」。
//
// 抽象出接口是为了：
//   - 测试可以注入 httptest 假服务或 MockClient，不联网；
//   - 生产可以换 OpenAI / DeepSeek / vLLM / Ollama 等任何 OpenAI 兼容端点；
//   - Agent 不关心 HTTP 细节，只做候选拼装、解析与回退。
type Client interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

const (
	// defaultBaseURL 是 OpenAI 官方兼容端点（需 APIKey）。
	defaultBaseURL = "https://api.openai.com/v1"
	// defaultModel 是默认模型（便宜且够用的演示默认）。
	defaultModel = "gpt-4o-mini"
	// maxResponseBytes 限制响应体大小，防止异常服务返回超大 body。
	maxResponseBytes = 1 << 20
)

// OpenAICompatClient 是 OpenAI 兼容的 /chat/completions 客户端。
//
// 兼容范围：OpenAI、DeepSeek、vLLM、Ollama（Ollama 的 OpenAI 兼容端点
// `http://127.0.0.1:11434/v1`）等；只有 baseURL / apiKey / model 三个关键配置。
type OpenAICompatClient struct {
	BaseURL     string
	APIKey      string
	Model       string
	HTTPClient  *http.Client
	Temperature float64
	MaxTokens   int
}

// NewOpenAICompatClient 构造客户端；baseURL/model 为空时用默认值。
//
// baseURL 应指向 `/v1` 这一级（如 `https://api.openai.com/v1`），
// 实际请求地址是 `{baseURL}/chat/completions`。
func NewOpenAICompatClient(baseURL, apiKey, model string) *OpenAICompatClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = defaultModel
	}
	return &OpenAICompatClient{
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIKey:      apiKey,
		Model:       model,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
		Temperature: 0,
		MaxTokens:   64,
	}
}

// NewFromEnv 按环境变量构造客户端；**没有 API key 时返回 MockClient**，不联网。
//
// 环境变量（后者是 OpenAI 生态的兼容名）：
//
//	LLM_BASE_URL | OPENAI_BASE_URL
//	LLM_API_KEY  | OPENAI_API_KEY
//	LLM_MODEL    | OPENAI_MODEL
func NewFromEnv() Client {
	apiKey := firstNonEmpty(os.Getenv("LLM_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return &MockClient{}
	}
	baseURL := firstNonEmpty(os.Getenv("LLM_BASE_URL"), os.Getenv("OPENAI_BASE_URL"))
	model := firstNonEmpty(os.Getenv("LLM_MODEL"), os.Getenv("OPENAI_MODEL"))
	return NewOpenAICompatClient(baseURL, apiKey, model)
}

// chatRequest 是 OpenAI 兼容 /chat/completions 的请求体（只带 demo 需要的字段）。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Complete 调用 /chat/completions 并返回 assistant 文本。
//
// 错误分为：请求构造/网络错误、非 2xx、响应 JSON 非法、choices 为空。
// 这些都交给 Agent 统一回退，不在这里重试（回退 FirstLegal 比盲目重试更可控）。
func (c *OpenAICompatClient) Complete(ctx context.Context, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("llm: prompt 不能为空")
	}
	body, err := json.Marshal(chatRequest{
		Model:       c.Model,
		Messages:    []chatMessage{{Role: "system", Content: SystemPrompt}, {Role: "user", Content: prompt}},
		Temperature: c.Temperature,
		MaxTokens:   c.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("llm: 编码请求失败: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("llm: 构造请求失败: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("llm: 请求失败: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("llm: 读取响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("llm: HTTP %d: %s", response.StatusCode, truncate(string(payload), 200))
	}
	var decoded chatResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("llm: 解析响应失败: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return "", errors.New("llm: 响应没有 choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	if content == "" {
		return "", errors.New("llm: 响应内容为空")
	}
	return content, nil
}

// chatURL 返回 chat/completions 的完整地址。
func (c *OpenAICompatClient) chatURL() string {
	base := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// MockClient 是不联网的 LLM 替身：从 prompt 的候选列表里选第一张。
//
// 用于「没有 API key 的 demo / CI」：决策链路、解析、回退、统计全部照常，
// 只是「模型」变成一个确定性规则。Response 非空时直接返回它（测试可注入非法牌）。
type MockClient struct {
	Response string
	calls    atomic.Int64
}

// Calls 返回 MockClient 被调用的次数。
func (m *MockClient) Calls() int64 { return m.calls.Load() }

// Complete 实现 Client：优先返回注入的 Response，否则取 CANDIDATES 第一张。
func (m *MockClient) Complete(ctx context.Context, prompt string) (string, error) {
	m.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(m.Response) != "" {
		return m.Response, nil
	}
	first := firstCandidateFromPrompt(prompt)
	if first == "" {
		return "", errors.New("mock: prompt 里没有候选牌")
	}
	return first, nil
}

// EstimateTokens 粗略估算一段文本的 token 数（无第三方 tokenizer 依赖）。
//
// 口径：ASCII 按 4 字符 ≈ 1 token，非 ASCII（中文等）按 1 字符 ≈ 1 token。
// 只用于 demo 展示与统计趋势，不参与任何正确性判断。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii, wide := 0, 0
	for _, r := range text {
		if r < 128 {
			ascii++
		} else {
			wide++
		}
	}
	tokens := ascii/4 + wide
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// truncate 截断过长文本（错误信息里避免打印整页 HTML）。
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
