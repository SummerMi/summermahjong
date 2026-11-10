package llm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// 决策来源（Explain / 评测统计用）。
const (
	sourceLLM                  = "llm"
	sourceFallbackNoCandidates = "fallback:no_candidates"
	sourceFallbackError        = "fallback:llm_error"
	sourceFallbackTimeout      = "fallback:llm_timeout"
	sourceFallbackInvalid      = "fallback:invalid_output"
)

// 编译期确认实现 agent.Agent（契约 §4）。
var _ agent.Agent = (*Agent)(nil)

// Stats 是 LLM Agent 的累计统计（只读快照，通过 Stats() 取）。
type Stats struct {
	Calls            int64 // 调 LLM 的次数（含失败）
	Fallbacks        int64 // 回退到 FirstLegal 的次数
	Invalid          int64 // 模型输出无法解析成合法牌的次数
	Errors           int64 // LLM 调用报错（网络 / HTTP / 超时）次数
	PromptTokens     int64 // prompt token 估算累计
	CompletionTokens int64 // 回复 token 估算累计
}

// Option 是 Agent 的可选配置。
type Option func(*Agent)

// WithTopK 设置交给 LLM 的候选数量（默认 5）。
func WithTopK(k int) Option {
	return func(a *Agent) {
		if k > 0 {
			a.topK = k
		}
	}
}

// WithTimeout 设置单次 LLM 调用的超时（默认 2s；引擎侧另有 500ms 的 DecideWithTimeout）。
func WithTimeout(timeout time.Duration) Option {
	return func(a *Agent) {
		if timeout > 0 {
			a.timeout = timeout
		}
	}
}

// WithFallback 设置回退 Agent（默认 agent.FirstLegal）。
func WithFallback(fallback agent.Agent) Option {
	return func(a *Agent) {
		if fallback != nil {
			a.fallback = fallback
		}
	}
}

// Agent 是「LLM 选牌」Agent：本地算 top-k 候选 → 拼 prompt → LLM 选 → 解析校验 → 失败回退。
//
// 契约约束（§4）：
//   - 只读 Observation，不接触 State；候选来自 obs.LegalActions；
//   - 返回的动作一定在 obs.LegalActions 内；
//   - 超时 / 报错 / 输出非法时回退 agent.FirstLegal，对调用方透明（error == nil）。
type Agent struct {
	client   Client
	fallback agent.Agent
	topK     int
	timeout  time.Duration

	mu    sync.Mutex
	stats Stats
	last  *decisionRecord
}

// decisionRecord 是最近一次决策的完整现场，供 Explain / 调试使用。
type decisionRecord struct {
	candidates []Candidate
	chosen     game.Action
	prompt     string
	response   string
	source     string
	reason     string
}

// New 构造 LLM Agent；client 为 nil 时使用 MockClient（无 key 的 demo 路径）。
func New(client Client, options ...Option) *Agent {
	if client == nil {
		client = &MockClient{}
	}
	instance := &Agent{
		client:   client,
		fallback: agent.FirstLegal{},
		topK:     5,
		timeout:  2 * time.Second,
	}
	for _, option := range options {
		option(instance)
	}
	return instance
}

// Name 实现 agent.Agent，用于回放 / 评测报告。
func (a *Agent) Name() string { return "llm" }

// Model 返回底模型的标识（MockClient 显示 "mock"）。
func (a *Agent) Model() string {
	if client, ok := a.client.(*OpenAICompatClient); ok {
		return client.Model
	}
	return "mock"
}

// Stats 返回统计快照。
func (a *Agent) Stats() Stats {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stats
}

// ResetStats 重置统计与最近一次决策记录（评测时按局清零）。
func (a *Agent) ResetStats() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stats = Stats{}
	a.last = nil
}

// LastPrompt 返回最近一次的 prompt（排查「模型到底看到了什么」）。
func (a *Agent) LastPrompt() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		return ""
	}
	return a.last.prompt
}

// Decide 实现 agent.Agent。
//
// 流程：候选计算 → prompt →（带超时）调 LLM → 解析成牌 → 合法性校验；
// 任一环节失败都回退到启发式首选，保证返回合法动作且 error == nil。
func (a *Agent) Decide(ctx context.Context, obs game.Observation) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		return game.Action{}, agent.ErrNoLegalAction
	}

	candidates := TopCandidates(obs, a.topK)
	if len(candidates) == 0 {
		action, err := a.fallbackAction(ctx, obs)
		if err != nil {
			return game.Action{}, err
		}
		a.addStats(func(s *Stats) { s.Fallbacks++ })
		a.storeDecision(&decisionRecord{
			chosen: action,
			source: sourceFallbackNoCandidates,
			reason: "当前没有可打出的候选（非摸打阶段），直接回退启发式",
		})
		return action, nil
	}

	prompt := BuildPrompt(obs, candidates)
	a.addStats(func(s *Stats) {
		s.Calls++
		s.PromptTokens += int64(EstimateTokens(prompt))
	})

	callCtx := ctx
	if a.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	started := time.Now()
	response, err := a.client.Complete(callCtx, prompt)
	if err != nil {
		source := sourceFallbackError
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			source = sourceFallbackTimeout
		}
		a.addStats(func(s *Stats) {
			s.Errors++
			s.Fallbacks++
		})
		action, fallbackErr := a.fallbackAction(ctx, obs)
		if fallbackErr != nil {
			return game.Action{}, fallbackErr
		}
		a.storeDecision(&decisionRecord{
			candidates: candidates,
			chosen:     action,
			prompt:     prompt,
			source:     source,
			reason:     "LLM 调用失败，回退启发式: " + err.Error(),
		})
		return action, nil
	}
	a.addStats(func(s *Stats) { s.CompletionTokens += int64(EstimateTokens(response)) })

	action, ok := pickAction(obs, response)
	if !ok {
		a.addStats(func(s *Stats) {
			s.Invalid++
			s.Fallbacks++
		})
		fallbackAction, fallbackErr := a.fallbackAction(ctx, obs)
		if fallbackErr != nil {
			return game.Action{}, fallbackErr
		}
		a.storeDecision(&decisionRecord{
			candidates: candidates,
			chosen:     fallbackAction,
			prompt:     prompt,
			response:   response,
			source:     sourceFallbackInvalid,
			reason:     "模型输出里没有合法可出的牌: " + truncate(response, 120),
		})
		return fallbackAction, nil
	}

	a.storeDecision(&decisionRecord{
		candidates: candidates,
		chosen:     action,
		prompt:     prompt,
		response:   response,
		source:     sourceLLM,
		reason:     fmt.Sprintf("模型选择 %s（耗时 %s）", action.Tile.String(), time.Since(started).Round(time.Millisecond)),
	})
	return action, nil
}

// fallbackAction 返回启发式首选：和了 > 荣和 > fallback Agent > 第一个合法动作。
func (a *Agent) fallbackAction(ctx context.Context, obs game.Observation) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		return game.Action{}, agent.ErrNoLegalAction
	}
	for _, kind := range []game.ActionKind{game.ActTsumo, game.ActRon} {
		for _, action := range obs.LegalActions {
			if action.Kind == kind {
				return action, nil
			}
		}
	}
	if a.fallback != nil {
		if action, err := a.fallback.Decide(ctx, obs); err == nil && agent.InLegalActions(obs, action) {
			return action, nil
		}
	}
	return obs.LegalActions[0], nil
}

// addStats 在锁内更新统计。
func (a *Agent) addStats(update func(*Stats)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	update(&a.stats)
}

// storeDecision 保存最近一次决策现场。
func (a *Agent) storeDecision(record *decisionRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.last = record
}

// Explain 返回最近一次决策的可读解释（教练模式：候选表 + 最终选择 + 来源）。
//
// decision 用于校验调用方问的是不是最近这次决策；不匹配时会在开头提示。
func (a *Agent) Explain(decision game.Action) string {
	a.mu.Lock()
	record := a.last
	stats := a.stats
	a.mu.Unlock()
	if record == nil {
		return "LLM Agent 还没有做出决策。"
	}
	var builder strings.Builder
	if record.chosen.Tile != decision.Tile || record.chosen.Kind != decision.Kind {
		fmt.Fprintf(&builder, "（注意：询问的动作与最近一次决策不同）\n")
	}
	fmt.Fprintf(&builder, "决策：%s %s（seat %d）\n", record.chosen.Kind, record.chosen.Tile.String(), record.chosen.Seat)
	fmt.Fprintf(&builder, "来源：%s\n", record.source)
	if record.reason != "" {
		fmt.Fprintf(&builder, "说明：%s\n", record.reason)
	}
	if len(record.candidates) > 0 {
		builder.WriteString("候选：\n")
		for _, candidate := range record.candidates {
			fmt.Fprintf(&builder, "  - %-4s 向听=%d 进张=%d 危险=%.1f 打点潜力=%d 综合=%.1f\n",
				candidate.Tile, candidate.Shanten, candidate.Ukeire, candidate.Danger, candidate.Potential, candidate.Score)
		}
	}
	if record.response != "" {
		fmt.Fprintf(&builder, "模型原始输出：%q\n", record.response)
	}
	fmt.Fprintf(&builder, "累计：调用=%d 回退=%d 非法输出=%d 错误=%d 估算 token=%d/%d\n",
		stats.Calls, stats.Fallbacks, stats.Invalid, stats.Errors, stats.PromptTokens, stats.CompletionTokens)
	return builder.String()
}

// alnumRunPattern 提取回复里的字母数字片段（用于从自然语言 / JSON 里抠出牌面）。
var alnumRunPattern = regexp.MustCompile(`[A-Za-z0-9]+`)

// parseTilesFromText 从模型回复里提取可能的牌面编码，按出现顺序返回。
//
// 两轮扫描：先认 2~3 字符的牌（3p / r5m），再认单字母字牌（E/S/W/N/P/F/C），
// 避免英文单词里的字母（如 choose 里的 c）被误认成「中」。
func parseTilesFromText(text string) []tile.Tile {
	runs := alnumRunPattern.FindAllString(text, -1)
	out := make([]tile.Tile, 0, 4)
	for _, run := range runs {
		if len(run) < 2 || len(run) > 3 {
			continue
		}
		if parsed, err := tile.ParseTile(run); err == nil {
			out = append(out, parsed)
		}
	}
	for _, run := range runs {
		if len(run) != 1 {
			continue
		}
		if parsed, err := tile.ParseTile(run); err == nil {
			out = append(out, parsed)
		}
	}
	return out
}

// pickAction 把模型回复解析成「合法的打牌动作」；没有合法候选时 ok=false。
func pickAction(obs game.Observation, response string) (game.Action, bool) {
	for _, parsed := range parseTilesFromText(response) {
		action := game.Action{Kind: game.ActDiscard, Seat: obs.Seat, Tile: parsed}
		if agent.InLegalActions(obs, action) {
			return action, true
		}
	}
	return game.Action{}, false
}
