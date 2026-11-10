package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// writeCompletion 往 httptest 响应里写一个 OpenAI 兼容的 chat completion。
func writeCompletion(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	}); err != nil {
		t.Errorf("write completion: %v", err)
	}
}

func TestNewFromEnvWithoutKeyReturnsMock(t *testing.T) {
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	client := NewFromEnv()
	if _, ok := client.(*MockClient); !ok {
		t.Fatalf("NewFromEnv() = %T, want *MockClient when no API key", client)
	}
}

func TestNewFromEnvWithKeyBuildsOpenAIClient(t *testing.T) {
	t.Setenv("LLM_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("LLM_BASE_URL", "https://example.test/v1/")
	t.Setenv("LLM_MODEL", "test-model")
	client, ok := NewFromEnv().(*OpenAICompatClient)
	if !ok {
		t.Fatalf("NewFromEnv() did not return *OpenAICompatClient")
	}
	if client.APIKey != "test-key" || client.Model != "test-model" {
		t.Fatalf("client config = %+v", client)
	}
	if client.BaseURL != "https://example.test/v1" {
		t.Fatalf("BaseURL = %q, trailing slash should be trimmed", client.BaseURL)
	}
}

func TestOpenAICompatClientDefaults(t *testing.T) {
	client := NewOpenAICompatClient("", "", "")
	if client.BaseURL != defaultBaseURL || client.Model != defaultModel {
		t.Fatalf("defaults = %+v", client)
	}
	if client.chatURL() != defaultBaseURL+"/chat/completions" {
		t.Fatalf("chatURL = %q", client.chatURL())
	}
	if got := NewOpenAICompatClient("http://x/v1/chat/completions", "k", "m").chatURL(); got != "http://x/v1/chat/completions" {
		t.Fatalf("chatURL should not duplicate suffix: %q", got)
	}
}

func TestOpenAICompatClientComplete(t *testing.T) {
	var gotAuth, gotModel string
	var gotMessages []chatMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotModel = request.Model
		gotMessages = request.Messages
		writeCompletion(t, w, "3p")
	}))
	defer server.Close()

	client := NewOpenAICompatClient(server.URL+"/v1", "secret-key", "fake-model")
	content, err := client.Complete(context.Background(), "请选牌")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if content != "3p" {
		t.Fatalf("content = %q", content)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotModel != "fake-model" {
		t.Fatalf("model = %q", gotModel)
	}
	if len(gotMessages) != 2 || gotMessages[0].Role != "system" || gotMessages[0].Content != SystemPrompt {
		t.Fatalf("messages = %+v", gotMessages)
	}
	if gotMessages[1].Role != "user" || gotMessages[1].Content != "请选牌" {
		t.Fatalf("user message = %+v", gotMessages[1])
	}
}

func TestOpenAICompatClientErrors(t *testing.T) {
	longBody := strings.Repeat("x", 500)
	cases := []struct {
		name    string
		prompt  string
		handler func(w http.ResponseWriter, r *http.Request)
		wantErr string
	}{
		{
			name:   "empty prompt",
			prompt: "   ",
			handler: func(w http.ResponseWriter, r *http.Request) {
				t.Error("should not issue request for empty prompt")
			},
			wantErr: "prompt",
		},
		{
			name:   "http 500",
			prompt: "p",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(longBody))
			},
			wantErr: "HTTP 500",
		},
		{
			name:   "bad json",
			prompt: "p",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("{"))
			},
			wantErr: "解析响应",
		},
		{
			name:   "no choices",
			prompt: "p",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[]}`))
			},
			wantErr: "choices",
		},
		{
			name:   "empty content",
			prompt: "p",
			handler: func(w http.ResponseWriter, r *http.Request) {
				writeCompletion(t, w, "   ")
			},
			wantErr: "内容为空",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(testCase.handler))
			defer server.Close()
			client := NewOpenAICompatClient(server.URL+"/v1", "", "m")
			_, err := client.Complete(context.Background(), testCase.prompt)
			if err == nil {
				t.Fatalf("Complete should fail")
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want contains %q", err, testCase.wantErr)
			}
		})
	}
}

func TestOpenAICompatClientRespectsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writeCompletion(t, w, "1m")
	}))
	defer server.Close()

	client := NewOpenAICompatClient(server.URL+"/v1", "", "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Complete(ctx, "p"); err == nil {
		t.Fatal("Complete should respect context deadline")
	}
}

func TestMockClient(t *testing.T) {
	ctx := context.Background()
	t.Run("picks first candidate", func(t *testing.T) {
		client := &MockClient{}
		prompt := "牌局上下文\nCANDIDATES: [\"3p\",\"7s\"]\n请只输出一张牌"
		got, err := client.Complete(ctx, prompt)
		if err != nil || got != "3p" {
			t.Fatalf("Complete = %q, %v", got, err)
		}
		if client.Calls() != 1 {
			t.Fatalf("Calls = %d", client.Calls())
		}
	})
	t.Run("injected response", func(t *testing.T) {
		client := &MockClient{Response: "E"}
		got, err := client.Complete(ctx, "ignored")
		if err != nil || got != "E" {
			t.Fatalf("Complete = %q, %v", got, err)
		}
	})
	t.Run("no candidates", func(t *testing.T) {
		if _, err := (&MockClient{}).Complete(ctx, "plain prompt"); err == nil {
			t.Fatal("Complete should fail when prompt has no candidates")
		}
	})
	t.Run("canceled context", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := (&MockClient{}).Complete(canceled, "p"); err == nil {
			t.Fatal("Complete should fail on canceled context")
		}
	})
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("empty tokens = %d", got)
	}
	if got := EstimateTokens("abcd"); got != 1 {
		t.Fatalf("ascii tokens = %d", got)
	}
	if got := EstimateTokens("日麻"); got != 2 {
		t.Fatalf("cjk tokens = %d", got)
	}
	if got := EstimateTokens("a"); got != 1 {
		t.Fatalf("min tokens = %d", got)
	}
	if got := EstimateTokens(strings.Repeat("x", 100)); got != 25 {
		t.Fatalf("long ascii tokens = %d", got)
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", " ", "x", "y"); got != "x" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty empty = %q", got)
	}
}
