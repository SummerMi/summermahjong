package agent_test

import (
	"context"
	"testing"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// discardObservation 构造一个「两家牌可打」的最小观察。
func discardObservation() game.Observation {
	return game.Observation{
		Seat: 0,
		Hand: []tile.Tile{{Kind: tile.Man1}, {Kind: tile.Man2}},
		LegalActions: []game.Action{
			{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man1}},
			{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man2}},
		},
	}
}

// slowAgent 故意不响应 ctx，模拟没有超时保护的第三方策略。
type slowAgent struct{ delay time.Duration }

func (slowAgent) Name() string { return "slow" }

func (a slowAgent) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	time.Sleep(a.delay)
	return obs.LegalActions[0], nil
}

// wrongTileAgent 返回一个不在合法集合里的牌面。
type wrongTileAgent struct{}

func (wrongTileAgent) Name() string { return "wrong_tile" }

func (wrongTileAgent) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	return game.Action{Kind: game.ActDiscard, Seat: obs.Seat, Tile: tile.Tile{Kind: tile.Chun}}, nil
}

// panicAgent 模拟策略内部 panic。
type panicAgent struct{}

func (panicAgent) Name() string { return "panic" }

func (panicAgent) Decide(context.Context, game.Observation) (game.Action, error) {
	panic("boom")
}

// fixedAgent 返回第 idx 个合法动作（用于验证正常路径不被回退覆盖）。
type fixedAgent struct{ idx int }

func (fixedAgent) Name() string { return "fixed" }

func (a fixedAgent) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	return obs.LegalActions[a.idx], nil
}

// TestTimeoutFallsBackToFirstLegal 验证超时回退：动作等于第一个合法动作且 error 为 nil。
func TestTimeoutFallsBackToFirstLegal(t *testing.T) {
	obs := discardObservation()
	start := time.Now()
	action, err := agent.DecideWithTimeout(slowAgent{delay: 300 * time.Millisecond}, obs, 20*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("超时回退不应返回 error: %v", err)
	}
	if action.Tile.Kind != tile.Man1 {
		t.Fatalf("超时回退动作 = %+v，want 第一个合法动作 1m", action)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("超时回退耗时 %v，没有在 20ms 超时附近返回", elapsed)
	}
}

// TestInvalidActionFallsBack 验证越权动作被丢弃并回退（契约 §4）。
func TestInvalidActionFallsBack(t *testing.T) {
	obs := discardObservation()
	action, err := agent.DecideWithTimeout(wrongTileAgent{}, obs, time.Second)
	if err != nil {
		t.Fatalf("越权动作应回退而不是报错: %v", err)
	}
	if !agent.InLegalActions(obs, action) {
		t.Fatalf("回退动作 %+v 不在合法集合内", action)
	}
	if action.Tile.Kind != tile.Man1 {
		t.Fatalf("回退动作 = %+v，want 1m", action)
	}
}

// TestPanicFallsBack 验证策略 panic 时服务不崩、回退到首选。
func TestPanicFallsBack(t *testing.T) {
	obs := discardObservation()
	action, err := agent.DecideWithTimeout(panicAgent{}, obs, time.Second)
	if err != nil {
		t.Fatalf("panic 应回退而不是报错: %v", err)
	}
	if !agent.InLegalActions(obs, action) {
		t.Fatalf("回退动作 %+v 不在合法集合内", action)
	}
}

// TestValidActionPassthrough 验证正常策略的动作原样返回（不被回退替换）。
func TestValidActionPassthrough(t *testing.T) {
	obs := discardObservation()
	action, err := agent.DecideWithTimeout(fixedAgent{idx: 1}, obs, time.Second)
	if err != nil {
		t.Fatalf("正常策略不应报错: %v", err)
	}
	if action.Tile.Kind != tile.Man2 {
		t.Fatalf("动作 = %+v，want 第二个合法动作 2m", action)
	}
}

// TestTimeoutWithoutLegalActions 验证没有合法动作时返回 ErrNoLegalAction。
func TestTimeoutWithoutLegalActions(t *testing.T) {
	if _, err := agent.DecideWithTimeout(fixedAgent{}, game.Observation{}, time.Second); err == nil {
		t.Fatal("没有合法动作时应返回错误")
	}
}

// TestCancelledContextFallsBack 验证父 context 取消时也回退。
func TestCancelledContextFallsBack(t *testing.T) {
	obs := discardObservation()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	action, err := agent.DecideWithContext(ctx, fixedAgent{idx: 1}, obs, time.Second)
	if err != nil {
		t.Fatalf("context 取消应回退而不是报错: %v", err)
	}
	if !agent.InLegalActions(obs, action) {
		t.Fatalf("回退动作 %+v 不在合法集合内", action)
	}
}

// TestZeroTimeoutFallsBack 验证 timeout<=0 时直接回退（不做任何计算）。
func TestZeroTimeoutFallsBack(t *testing.T) {
	obs := discardObservation()
	action, err := agent.DecideWithTimeout(fixedAgent{idx: 1}, obs, 0)
	if err != nil {
		t.Fatalf("timeout<=0 应回退而不是报错: %v", err)
	}
	if action.Tile.Kind != tile.Man1 {
		t.Fatalf("回退动作 = %+v，want 1m", action)
	}
}

// TestNilAgentFallsBack 验证 a 为 nil 时不 panic、回退到首选。
func TestNilAgentFallsBack(t *testing.T) {
	obs := discardObservation()
	action, err := agent.DecideWithTimeout(nil, obs, time.Second)
	if err != nil {
		t.Fatalf("nil agent 应回退而不是报错: %v", err)
	}
	if !agent.InLegalActions(obs, action) {
		t.Fatalf("回退动作 %+v 不在合法集合内", action)
	}
}
