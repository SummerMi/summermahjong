package agent

import (
	"context"
	"testing"
	"time"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// legalDiscards 按手牌构造「全部可打」的合法动作集合。
func legalDiscards(hand []tile.Tile) []game.Action {
	acts := make([]game.Action, 0, len(hand))
	for _, t := range hand {
		acts = append(acts, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: t})
	}
	return acts
}

// TestAgentsRespectCancelledContext 覆盖各策略入口的 ctx 检查。
func TestAgentsRespectCancelledContext(t *testing.T) {
	hand := mustParse(t, "123m456p789s11p45s9s")
	obs := game.Observation{Seat: 0, Hand: hand, LegalActions: legalDiscards(hand)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	agents := []Agent{
		NewShantenAgent(StyleAttack),
		NewDefenseAgent(StyleDefense),
		NewSearchAgent(StyleClosed),
	}
	for _, ag := range agents {
		if _, err := ag.Decide(ctx, obs); err == nil {
			t.Fatalf("%s 在 ctx 取消后应返回错误", ag.Name())
		}
	}
	// 超时包装层在 ctx 取消时回退到合法动作。
	action, err := DecideWithContext(ctx, NewShantenAgent(StyleAttack), obs, time.Second)
	if err != nil {
		t.Fatalf("DecideWithContext: %v", err)
	}
	if !InLegalActions(obs, action) {
		t.Fatalf("回退动作 %+v 非法", action)
	}
}

// TestAgentsPickDrawWhenNoDiscards 覆盖「M1 摸牌阶段」：没有打牌候选时回退到摸牌。
func TestAgentsPickDrawWhenNoDiscards(t *testing.T) {
	hand := mustParse(t, "123m456p789s11p45s")
	obs := game.Observation{
		Seat:         0,
		Hand:         hand,
		LegalActions: []game.Action{{Kind: game.ActDraw, Seat: 0}},
	}
	for _, ag := range []Agent{
		NewShantenAgent(StyleAttack),
		NewDefenseAgent(StyleDefense),
		NewSearchAgent(StyleClosed),
	} {
		action, err := ag.Decide(context.Background(), obs)
		if err != nil {
			t.Fatalf("%s: %v", ag.Name(), err)
		}
		if action.Kind != game.ActDraw {
			t.Fatalf("%s 应摸牌，实际 %+v", ag.Name(), action)
		}
	}
}

// TestAgentsHandleEmptyHand 覆盖空手牌（异常状态）时也返回合法动作。
func TestAgentsHandleEmptyHand(t *testing.T) {
	obs := game.Observation{
		Seat:         0,
		Hand:         nil,
		LegalActions: []game.Action{{Kind: game.ActPass, Seat: 0}},
	}
	for _, ag := range []Agent{
		NewShantenAgent(StyleAttack),
		NewDefenseAgent(StyleDefense),
		NewSearchAgent(StyleClosed),
	} {
		action, err := ag.Decide(context.Background(), obs)
		if err != nil {
			t.Fatalf("%s: %v", ag.Name(), err)
		}
		if !InLegalActions(obs, action) {
			t.Fatalf("%s 返回非法动作 %+v", ag.Name(), action)
		}
	}
}

// TestDefenseAgentPrefersGenbutsu 验证防守策略在有立直者时优先现物。
func TestDefenseAgentPrefersGenbutsu(t *testing.T) {
	hand := mustParse(t, "123m456m789m11p45s9s")
	obs := game.Observation{
		Seat:         0,
		Hand:         hand,
		Riichi:       [4]bool{1: true},
		LegalActions: legalDiscards(hand),
		Discards: [4][]tile.Tile{
			1: {{Kind: tile.Man1}},
		},
	}
	action, err := NewDefenseAgent(StyleDefense).Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Tile.Kind != tile.Man1 {
		t.Fatalf("有立直者时 1m 是现物，应优先打出，实际 %+v", action)
	}
	if !InLegalActions(obs, action) {
		t.Fatalf("返回非法动作 %+v", action)
	}
}

// TestDefenseAgentWithoutRiichiUsesEfficiency 覆盖「无人立直」分支：
// 危险度为 0，退化为效率优先（应切掉明显无用的孤张字牌）。
func TestDefenseAgentWithoutRiichiUsesEfficiency(t *testing.T) {
	hand := mustParse(t, "123m456m789m11p45s1z")
	obs := game.Observation{Seat: 0, Hand: hand, LegalActions: legalDiscards(hand)}
	action, err := NewDefenseAgent(StyleDefense).Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Tile.Kind != tile.East {
		t.Fatalf("无立直时应按效率切孤张字牌 1z，实际 %+v", action)
	}
}

// TestShantenAgentPrefersEfficientDiscard 验证向听策略会切掉孤张字牌。
func TestShantenAgentPrefersEfficientDiscard(t *testing.T) {
	hand := mustParse(t, "123m456m789m11p45s1z")
	obs := game.Observation{Seat: 0, Hand: hand, LegalActions: legalDiscards(hand)}
	action, err := NewShantenAgent(StyleAttack).Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if action.Tile.Kind != tile.East {
		t.Fatalf("应向听最优切孤张字牌 1z，实际 %+v", action)
	}
}
