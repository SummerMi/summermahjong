package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
)

// pickLastAgent 是测试用回退策略：总是返回最后一个合法动作。
type pickLastAgent struct{}

func (pickLastAgent) Name() string { return "pick_last" }

func (pickLastAgent) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	return obs.LegalActions[len(obs.LegalActions)-1], nil
}

// fakeOpenAI 起一个返回固定内容的 OpenAI 兼容假服务。
func fakeOpenAI(t *testing.T, content string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeCompletion(t, w, content)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAgentWithMockClient(t *testing.T) {
	mock := &MockClient{}
	a := New(mock)
	obs := makeObs(t, defaultHand14)

	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !agent.InLegalActions(obs, action) {
		t.Fatalf("action %+v not legal", action)
	}
	stats := a.Stats()
	if stats.Calls != 1 || stats.Fallbacks != 0 || stats.Invalid != 0 || stats.Errors != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.PromptTokens == 0 || stats.CompletionTokens == 0 {
		t.Fatalf("token estimates not recorded: %+v", stats)
	}
	if a.Name() != "llm" || a.Model() != "mock" {
		t.Fatalf("name/model = %q/%q", a.Name(), a.Model())
	}
	if a.LastPrompt() == "" {
		t.Fatalf("LastPrompt should be recorded")
	}
	explain := a.Explain(action)
	for _, want := range []string{"来源：llm", "候选", "累计"} {
		if !strings.Contains(explain, want) {
			t.Fatalf("explain missing %q:\n%s", want, explain)
		}
	}
}

func TestAgentNilClientUsesMock(t *testing.T) {
	a := New(nil)
	if _, ok := a.client.(*MockClient); !ok {
		t.Fatalf("nil client should fall back to MockClient, got %T", a.client)
	}
}

func TestAgentBeforeDecisionExplain(t *testing.T) {
	if got := New(&MockClient{}).Explain(game.Action{}); !strings.Contains(got, "还没有") {
		t.Fatalf("Explain before decision = %q", got)
	}
}

func TestAgentNormalSelectionFakeOpenAI(t *testing.T) {
	var captured chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeCompletion(t, w, "4p")
	}))
	t.Cleanup(server.Close)

	client := NewOpenAICompatClient(server.URL+"/v1", "key", "test-model")
	a := New(client)
	obs := makeObs(t, defaultHand14)
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Tile.String() != "4p" || action.Kind != game.ActDiscard {
		t.Fatalf("action = %+v, want discard 4p", action)
	}
	stats := a.Stats()
	if stats.Calls != 1 || stats.Fallbacks != 0 || stats.Errors != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if captured.Model != "test-model" {
		t.Fatalf("captured model = %q", captured.Model)
	}
	if len(captured.Messages) != 2 || captured.Messages[0].Content != SystemPrompt {
		t.Fatalf("captured messages = %+v", captured.Messages)
	}
	if !strings.Contains(captured.Messages[1].Content, "CANDIDATES: ") {
		t.Fatalf("user prompt missing candidates:\n%s", captured.Messages[1].Content)
	}
	if got := a.Model(); got != "test-model" {
		t.Fatalf("Model = %q", got)
	}
}

func TestAgentTopKOptionControlsPrompt(t *testing.T) {
	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		prompt = request.Messages[1].Content
		writeCompletion(t, w, "4p")
	}))
	t.Cleanup(server.Close)

	a := New(NewOpenAICompatClient(server.URL+"/v1", "", "m"), WithTopK(2))
	if _, err := a.Decide(context.Background(), makeObs(t, defaultHand14)); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if count := strings.Count(prompt, `"tile":`); count != 2 {
		t.Fatalf("prompt candidates = %d, want 2:\n%s", count, prompt)
	}
}

func TestAgentInvalidOutputFallsBack(t *testing.T) {
	a := New(NewOpenAICompatClient(fakeOpenAI(t, "我不会打牌").URL+"/v1", "", "m"))
	obs := makeObs(t, defaultHand14)
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Tile != obs.LegalActions[0].Tile {
		t.Fatalf("fallback action = %+v, want first legal %+v", action, obs.LegalActions[0])
	}
	stats := a.Stats()
	if stats.Invalid != 1 || stats.Fallbacks != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if explain := a.Explain(action); !strings.Contains(explain, "回退") {
		t.Fatalf("explain should mention fallback:\n%s", explain)
	}
}

func TestAgentIllegalTileFallsBack(t *testing.T) {
	a := New(NewOpenAICompatClient(fakeOpenAI(t, "9s").URL+"/v1", "", "m"))
	obs := makeObs(t, defaultHand14)
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Tile.String() == "9s" {
		t.Fatalf("illegal tile should not be returned")
	}
	if a.Stats().Invalid != 1 {
		t.Fatalf("stats = %+v", a.Stats())
	}
}

func TestAgentHTTP500FallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	t.Cleanup(server.Close)

	a := New(NewOpenAICompatClient(server.URL+"/v1", "", "m"))
	action, err := a.Decide(context.Background(), makeObs(t, defaultHand14))
	if err != nil {
		t.Fatalf("Decide should be transparent on LLM error: %v", err)
	}
	if !agent.InLegalActions(makeObs(t, defaultHand14), action) {
		t.Fatalf("fallback action not legal: %+v", action)
	}
	stats := a.Stats()
	if stats.Errors != 1 || stats.Fallbacks != 1 || stats.Calls != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAgentTimeoutFallsBack(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		writeCompletion(t, w, "4p")
	}))
	t.Cleanup(server.Close)

	a := New(NewOpenAICompatClient(server.URL+"/v1", "", "m"), WithTimeout(20*time.Millisecond))
	obs := makeObs(t, defaultHand14)
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide should fall back on timeout: %v", err)
	}
	if !agent.InLegalActions(obs, action) {
		t.Fatalf("fallback action not legal: %+v", action)
	}
	stats := a.Stats()
	if stats.Errors != 1 || stats.Fallbacks != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if explain := a.Explain(action); !strings.Contains(explain, "llm_timeout") {
		t.Fatalf("explain should record timeout source:\n%s", explain)
	}
}

func TestAgentNoLegalActions(t *testing.T) {
	a := New(&MockClient{})
	if _, err := a.Decide(context.Background(), game.Observation{}); !errors.Is(err, agent.ErrNoLegalAction) {
		t.Fatalf("err = %v, want ErrNoLegalAction", err)
	}
}

func TestAgentNonDiscardFallsBackToTsumo(t *testing.T) {
	a := New(&MockClient{})
	obs := makeObs(t, defaultHand14)
	obs.LegalActions = []game.Action{{Kind: game.ActTsumo, Seat: obs.Seat}}
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Kind != game.ActTsumo {
		t.Fatalf("action = %+v, want tsumo", action)
	}
	stats := a.Stats()
	if stats.Calls != 0 || stats.Fallbacks != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestAgentFallbackPrefersRon(t *testing.T) {
	a := New(&MockClient{})
	obs := makeObs(t, defaultHand14)
	obs.LegalActions = []game.Action{
		{Kind: game.ActDiscard, Seat: obs.Seat, Tile: mustTiles(t, "1m")[0]},
		{Kind: game.ActRon, Seat: obs.Seat},
	}
	action, err := a.fallbackAction(context.Background(), obs)
	if err != nil || action.Kind != game.ActRon {
		t.Fatalf("fallbackAction = %+v, %v", action, err)
	}
}

func TestAgentCustomFallback(t *testing.T) {
	a := New(NewOpenAICompatClient(fakeOpenAI(t, "garbage").URL+"/v1", "", "m"), WithFallback(pickLastAgent{}))
	obs := makeObs(t, defaultHand14)
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	want := obs.LegalActions[len(obs.LegalActions)-1]
	if action.Tile != want.Tile {
		t.Fatalf("action = %+v, want custom fallback %+v", action, want)
	}
}

func TestAgentOptionsIgnoredInvalidValues(t *testing.T) {
	a := New(nil, WithTopK(0), WithTimeout(-1*time.Second), WithFallback(nil))
	if a.topK != 5 {
		t.Fatalf("topK = %d, want default 5", a.topK)
	}
	if a.timeout != 2*time.Second {
		t.Fatalf("timeout = %v, want default 2s", a.timeout)
	}
	if a.fallback == nil {
		t.Fatalf("fallback should keep default FirstLegal")
	}
}

func TestAgentStatsResetAndExplainMismatch(t *testing.T) {
	a := New(&MockClient{})
	obs := makeObs(t, defaultHand14)
	action, err := a.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if explain := a.Explain(game.Action{Kind: game.ActDiscard, Tile: mustTiles(t, "9m")[0]}); !strings.Contains(explain, "注意") {
		t.Fatalf("Explain should warn on mismatched action:\n%s", explain)
	}
	a.ResetStats()
	if stats := a.Stats(); stats != (Stats{}) {
		t.Fatalf("stats after reset = %+v", stats)
	}
	if a.LastPrompt() != "" {
		t.Fatalf("LastPrompt after reset = %q", a.LastPrompt())
	}
	if got := a.Explain(action); !strings.Contains(got, "还没有") {
		t.Fatalf("Explain after reset = %q", got)
	}
}
