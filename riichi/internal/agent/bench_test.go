package agent_test

import (
	"context"
	"testing"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// sinkAction 防止基准里的决策被编译器优化掉。
var sinkAction game.Action

// benchmarkObservation 构造一个 14 张手牌、可打牌的观察。
func benchmarkObservation(tb testing.TB) game.Observation {
	tb.Helper()
	hand, err := tile.ParseHand("123m456p789s11p45s9s")
	if err != nil {
		tb.Fatalf("tile.ParseHand: %v", err)
	}
	legal := make([]game.Action, 0, len(hand))
	for _, tl := range hand {
		legal = append(legal, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: tl})
	}
	return game.Observation{Seat: 0, Hand: hand, LegalActions: legal}
}

// benchmarkDefenseObservation 在基础观察上加上「有人立直 + 舍牌历史」，
// 让防守策略真的走危险度模型（否则危险度恒为 0，基准测不到主要路径）。
func benchmarkDefenseObservation(tb testing.TB) game.Observation {
	tb.Helper()
	obs := benchmarkObservation(tb)
	obs.Riichi[1] = true
	obs.Discards[1] = []tile.Tile{
		{Kind: tile.Man1},
		{Kind: tile.Pin9},
		{Kind: tile.Sou5},
	}
	return obs
}

func benchmarkAgent(b *testing.B, name string, obs game.Observation) {
	b.Helper()
	ag, err := agent.New(name, agent.Options{Seed: 1})
	if err != nil {
		b.Fatalf("New(%q): %v", name, err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	var action game.Action
	for i := 0; i < b.N; i++ {
		action, _ = ag.Decide(ctx, obs)
	}
	b.StopTimer()
	sinkAction = action
}

// BenchmarkShantenAgent 测向听策略单步决策（14 张手牌 → 选一张打出）。
func BenchmarkShantenAgent(b *testing.B) {
	benchmarkAgent(b, "shanten:attack", benchmarkObservation(b))
}

// BenchmarkDefenseAgent 测防守策略在有立直者时的单步决策（危险度模型主路径）。
func BenchmarkDefenseAgent(b *testing.B) {
	benchmarkAgent(b, "defense:defense", benchmarkDefenseObservation(b))
}

// BenchmarkSearchAgent 测限深搜索策略单步决策（要求 P95 < 5ms，基准提供 ns/op）。
func BenchmarkSearchAgent(b *testing.B) {
	benchmarkAgent(b, "search:closed", benchmarkObservation(b))
}
