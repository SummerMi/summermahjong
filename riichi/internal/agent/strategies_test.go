package agent_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

var bg = context.Background()

// playObservationStream 用随机策略跑一局，收集每个决策点的 Observation。
//
// 这是「随机牌谱」测试的公共输入：合法率、确定性、延迟都复用它，
// 保证测的是真实对局里出现的 13/14 张手牌，而不是手工构造的玩具数据。
func playObservationStream(t *testing.T, seed int64, maxSteps int) []game.Observation {
	t.Helper()
	state, err := game.New(seed, game.DefaultRuleset())
	if err != nil {
		t.Fatalf("game.New(%d): %v", seed, err)
	}
	chooser := agent.NewRandom(seed)
	stream := make([]game.Observation, 0, maxSteps)
	for step := 0; step < maxSteps; step++ {
		if state.Phase == game.PhaseRoundEnd || state.Phase == game.PhaseGameEnd {
			break
		}
		seat := state.CurrentSeat()
		obs := state.Observe(seat)
		if len(obs.LegalActions) == 0 {
			t.Fatalf("step %d: 当前座位没有合法动作", step)
		}
		stream = append(stream, obs)
		action, err := chooser.Decide(bg, obs)
		if err != nil {
			t.Fatalf("step %d: 随机策略决策失败: %v", step, err)
		}
		if !agent.InLegalActions(obs, action) {
			t.Fatalf("step %d: 随机策略返回非法动作 %+v", step, action)
		}
		if err := state.Apply(action); err != nil {
			if state.Phase == game.PhaseRoundEnd {
				break
			}
			t.Fatalf("step %d: Apply 失败: %v", step, err)
		}
	}
	if len(stream) < 30 {
		t.Fatalf("随机牌谱太短（%d 个决策点），无法用于统计", len(stream))
	}
	return stream
}

// discardObservations 只保留「需要打牌」的观察（14 张手牌形态）。
func discardObservations(stream []game.Observation) []game.Observation {
	out := make([]game.Observation, 0, len(stream))
	for _, obs := range stream {
		for _, a := range obs.LegalActions {
			if a.Kind == game.ActDiscard {
				out = append(out, obs)
				break
			}
		}
	}
	return out
}

func decodeActions(t *testing.T, spec []string) [4]agent.Agent {
	t.Helper()
	var agents [4]agent.Agent
	for i, name := range spec {
		a, err := agent.New(name, agent.Options{Seed: int64(i + 1)})
		if err != nil {
			t.Fatalf("构造 %q 失败: %v", name, err)
		}
		agents[i] = a
	}
	return agents
}

// playUntilEnd 用给定的四家策略打完一局摸打循环；返回实际执行的动作序列。
func playUntilEnd(t *testing.T, seed int64, agents [4]agent.Agent) []game.Action {
	t.Helper()
	state, err := game.New(seed, game.DefaultRuleset())
	if err != nil {
		t.Fatalf("game.New(%d): %v", seed, err)
	}
	actions := make([]game.Action, 0, 256)
	for step := 0; step < 1000; step++ {
		if state.Phase == game.PhaseRoundEnd || state.Phase == game.PhaseGameEnd {
			return actions
		}
		if got := state.TotalTiles(); got != tile.DeckSize {
			t.Fatalf("第 %d 步牌数不守恒: %d != %d", step, got, tile.DeckSize)
		}
		seat := state.CurrentSeat()
		obs := state.Observe(seat)
		action, err := agent.DecideWithTimeout(agents[seat], obs, 500*time.Millisecond)
		if err != nil {
			t.Fatalf("第 %d 步策略 %q 决策失败: %v", step, agents[seat].Name(), err)
		}
		if !agent.InLegalActions(obs, action) {
			t.Fatalf("第 %d 步策略 %q 返回非法动作 %+v", step, agents[seat].Name(), action)
		}
		if err := state.Apply(action); err != nil {
			if state.Phase == game.PhaseRoundEnd {
				return actions
			}
			t.Fatalf("第 %d 步 Apply 失败: %v", step, err)
		}
		actions = append(actions, action)
	}
	t.Fatalf("对局在 1000 步内没有结束（死循环？）")
	return nil
}

// TestAllAgentsPickLegalActionsOnRandomGame 是「决策合法率 100%」的验收测试：
// 在随机牌谱的每个决策点，所有注册策略（含打风后缀）都必须返回合法动作。
func TestAllAgentsPickLegalActionsOnRandomGame(t *testing.T) {
	stream := playObservationStream(t, 20261110, 400)
	names := agent.RegisteredNames()
	if len(names) < 10 {
		t.Fatalf("注册策略太少: %v", names)
	}
	for _, name := range names {
		ag, err := agent.New(name, agent.Options{Seed: 7})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		for i, obs := range stream {
			action, err := agent.DecideWithTimeout(ag, obs, 500*time.Millisecond)
			if err != nil {
				t.Fatalf("%s 第 %d 个决策点失败: %v", name, i, err)
			}
			if !agent.InLegalActions(obs, action) {
				t.Fatalf("%s 第 %d 个决策点返回非法动作 %+v", name, i, action)
			}
		}
	}
}

// TestAgentsDeterministic 验收「同 seed 同决策」：同名同 seed 的两次构造，
// 对同一串观察必须给出完全一致的动作（random 由 seed 决定，其余策略本身确定性）。
func TestAgentsDeterministic(t *testing.T) {
	stream := playObservationStream(t, 42, 400)
	for _, name := range agent.RegisteredNames() {
		a, err := agent.New(name, agent.Options{Seed: 123})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		b, err := agent.New(name, agent.Options{Seed: 123})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		for i, obs := range stream {
			x, err := a.Decide(bg, obs)
			if err != nil {
				t.Fatalf("%s 第 %d 个决策点失败: %v", name, i, err)
			}
			y, err := b.Decide(bg, obs)
			if err != nil {
				t.Fatalf("%s 第 %d 个决策点失败: %v", name, i, err)
			}
			if x.Kind != y.Kind || x.Tile != y.Tile || x.Seat != y.Seat {
				t.Fatalf("%s 第 %d 个决策点不确定: %+v vs %+v", name, i, x, y)
			}
		}
	}
}

// TestSameSeedSameGame 是更端到端的确定性：同 seed + 同策略配置，两局动作序列一致。
func TestSameSeedSameGame(t *testing.T) {
	specs := []string{"shanten:attack", "defense:defense", "search:closed", "random"}
	for _, seed := range []int64{1, 2} {
		// 每局重建策略：random 的内部 rng 会随决策推进，不能跨局复用。
		a := playUntilEnd(t, seed, decodeActions(t, specs))
		b := playUntilEnd(t, seed, decodeActions(t, specs))
		if len(a) != len(b) {
			t.Fatalf("seed %d: 动作数不一致 %d vs %d", seed, len(a), len(b))
		}
		for i := range a {
			if a[i].Kind != b[i].Kind || a[i].Tile != b[i].Tile {
				t.Fatalf("seed %d 第 %d 步动作不一致: %+v vs %+v", seed, i, a[i], b[i])
			}
		}
	}
}

// TestFourAgentsCompleteFullGame 验收「四个 agent 用 game 的摸打循环打完整一局不报错」。
func TestFourAgentsCompleteFullGame(t *testing.T) {
	specs := []string{"shanten:attack", "defense:defense", "search:closed", "random"}
	for _, seed := range []int64{20261110, 3, 7} {
		agents := decodeActions(t, specs)
		actions := playUntilEnd(t, seed, agents)
		if len(actions) == 0 {
			t.Fatalf("seed %d: 一局没有任何动作", seed)
		}
		// 摸打循环里摸牌与打牌必须交替出现，且数量守恒：70 次摸牌后牌山耗尽。
		draws := 0
		for _, a := range actions {
			if a.Kind == game.ActDraw {
				draws++
			}
		}
		if draws == 0 {
			t.Fatalf("seed %d: 没有摸牌动作", seed)
		}
		if draws > tile.MaxDrawsFromLive {
			t.Fatalf("seed %d: 摸牌次数 %d 超过活牌上限 %d", seed, draws, tile.MaxDrawsFromLive)
		}
	}
}

// latencyCase 是一条延迟验收项。
type latencyCase struct {
	name  string
	limit time.Duration
}

// TestDecisionLatencyP95 用 1000 次采样统计单步决策 P95：
// 所有策略 P95 < 20ms，搜索策略 < 5ms（契约 §4 的 500ms 上限之外的工程目标）。
func TestDecisionLatencyP95(t *testing.T) {
	stream := discardObservations(playObservationStream(t, 99, 400))
	if len(stream) < 50 {
		t.Fatalf("可打牌的观察太少: %d", len(stream))
	}
	cases := []latencyCase{
		{name: "shanten:attack", limit: 20 * time.Millisecond},
		{name: "shanten:closed", limit: 20 * time.Millisecond},
		{name: "defense:defense", limit: 20 * time.Millisecond},
		{name: "search:closed", limit: 5 * time.Millisecond},
	}
	for _, tc := range cases {
		ag, err := agent.New(tc.name, agent.Options{Seed: 1})
		if err != nil {
			t.Fatalf("New(%q): %v", tc.name, err)
		}
		// 预热，避免首次调用的内存分配/分支预测影响统计。
		for i := 0; i < 20; i++ {
			if _, err := ag.Decide(bg, stream[i%len(stream)]); err != nil {
				t.Fatalf("%s 预热失败: %v", tc.name, err)
			}
		}
		samples := 1000
		durations := make([]time.Duration, 0, samples)
		for i := 0; i < samples; i++ {
			obs := stream[i%len(stream)]
			start := time.Now()
			if _, err := ag.Decide(bg, obs); err != nil {
				t.Fatalf("%s 第 %d 次决策失败: %v", tc.name, i, err)
			}
			durations = append(durations, time.Since(start))
		}
		p50 := percentile(durations, 0.50)
		p95 := percentile(durations, 0.95)
		t.Logf("%s: p50=%v p95=%v (limit %v)", tc.name, p50, p95, tc.limit)
		if p95 > tc.limit {
			t.Fatalf("%s p95=%v 超过上限 %v", tc.name, p95, tc.limit)
		}
	}
}

// percentile 返回排序后的第 p 分位（p ∈ [0,1]）。
func percentile(durations []time.Duration, p float64) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// TestAgentsHandleFutureActionKinds 验证「未来动作先设计接口」：
// 当合法集合里只有 M2 才有的动作（立直/过）时，内置策略也要回退到合法项，
// 不能把未知 ActionKind 当成打牌来评估。
func TestAgentsHandleFutureActionKinds(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		Hand: []tile.Tile{{Kind: tile.Man1}, {Kind: tile.Man2}},
		LegalActions: []game.Action{
			{Kind: game.ActRiichi, Seat: 0},
			{Kind: game.ActPass, Seat: 0},
		},
	}
	for _, name := range []string{"shanten:attack", "defense:defense", "search:closed"} {
		ag, err := agent.New(name, agent.Options{})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		action, err := ag.Decide(bg, obs)
		if err != nil {
			t.Fatalf("%s 决策失败: %v", name, err)
		}
		if !agent.InLegalActions(obs, action) {
			t.Fatalf("%s 返回非法动作 %+v", name, action)
		}
	}
}

// TestAgentsWithoutLegalActions 验证没有合法动作时统一返回 ErrNoLegalAction。
func TestAgentsWithoutLegalActions(t *testing.T) {
	obs := game.Observation{Seat: 0}
	for _, name := range []string{"random", "shanten:attack", "defense:defense", "search:closed"} {
		ag, err := agent.New(name, agent.Options{})
		if err != nil {
			t.Fatalf("New(%q): %v", name, err)
		}
		if _, err := ag.Decide(bg, obs); err == nil {
			t.Fatalf("%s 应在无合法动作时报错", name)
		}
		if _, err := agent.DecideWithTimeout(ag, obs, time.Second); err == nil {
			t.Fatalf("%s DecideWithTimeout 应在无合法动作时报错", name)
		}
	}
}

// TestRandomStyleSuffixAccepted 验证 factory 对 random:<style> 的兼容处理
// （random 没有可调参数，接受后缀但不改变行为）。
func TestRandomStyleSuffixAccepted(t *testing.T) {
	ag, err := agent.New("random:attack", agent.Options{Seed: 3})
	if err != nil {
		t.Fatalf("random:attack 应被接受: %v", err)
	}
	if ag.Name() != "random" {
		t.Fatalf("random 的 Name()=%q want random", ag.Name())
	}
}

// TestDecisionErrorPaths 覆盖「策略返回错误」时 DecideWithTimeout 的回退。
func TestDecisionErrorPaths(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		LegalActions: []game.Action{
			{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man1}},
			{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man2}},
		},
	}
	action, err := agent.DecideWithTimeout(errAgent{}, obs, time.Second)
	if err != nil {
		t.Fatalf("错误应回退而不是返回 error: %v", err)
	}
	if action.Tile.Kind != tile.Man1 {
		t.Fatalf("回退动作 = %+v want 第一个合法动作 1m", action)
	}
}

// errAgent 是总是返回错误的测试策略。
type errAgent struct{}

func (errAgent) Name() string { return "err" }

func (errAgent) Decide(context.Context, game.Observation) (game.Action, error) {
	return game.Action{}, fmt.Errorf("boom")
}
