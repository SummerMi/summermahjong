package agent

import (
	"testing"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// ── 响应窗口的选择性鸣牌（2026-11-16）──────────────────────────

func testHand(t *testing.T, text string) []tile.Tile {
	t.Helper()
	hand, err := tile.ParseHand(text)
	if err != nil {
		t.Fatalf("解析手牌 %q: %v", text, err)
	}
	return hand
}

// TestDecideResponseAlwaysRons 有荣和必取。
func TestDecideResponseAlwaysRons(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		Hand: testHand(t, "123m 456m 789m 45p 7p 9p"),
		Responses: []game.Action{
			{Kind: game.ActPon, Seat: 0, Tile: tile.Tile{Kind: tile.Pin5}},
			{Kind: game.ActRon, Seat: 0, Tile: tile.Tile{Kind: tile.Pin3}},
			{Kind: game.ActPass, Seat: 0},
		},
	}
	if got := decideResponseAction(obs, StyleAttack); got.Kind != game.ActRon {
		t.Fatalf("有荣和应取荣和，实际 %v", got.Kind)
	}
}

// TestDecideResponseCallsWhenShantenImproves 攻击打风：鸣牌后向听变好 → 鸣。
func TestDecideResponseCallsWhenShantenImproves(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		Hand: testHand(t, "123m 456m 789m 45p 7p 9p"),
		Responses: []game.Action{
			{Kind: game.ActChi, Seat: 0, Tile: tile.Tile{Kind: tile.Pin3},
				Consumed: []tile.Tile{{Kind: tile.Pin4}, {Kind: tile.Pin5}}},
			{Kind: game.ActPass, Seat: 0},
		},
	}
	if got := decideResponseAction(obs, StyleAttack); got.Kind != game.ActChi {
		t.Fatalf("鸣后向听变好应选择吃，实际 %v", got.Kind)
	}
}

// TestDecideResponsePassesWhenShantenSame 攻击打风：鸣牌不改善向听 → pass。
func TestDecideResponsePassesWhenShantenSame(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		Hand: testHand(t, "123m 456m 1z1z 5p7p 2s4s 8s"),
		Responses: []game.Action{
			{Kind: game.ActPon, Seat: 0, Tile: tile.Tile{Kind: tile.East},
				Consumed: []tile.Tile{{Kind: tile.East}, {Kind: tile.East}}},
			{Kind: game.ActPass, Seat: 0},
		},
	}
	if got := decideResponseAction(obs, StyleAttack); got.Kind != game.ActPass {
		t.Fatalf("鸣牌不改善向听应 pass，实际 %v", got.Kind)
	}
}

// TestDecideResponseClosedNeverCalls 门清打风不鸣。
func TestDecideResponseClosedNeverCalls(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		Hand: testHand(t, "123m 456m 789m 45p 7p 9p"),
		Responses: []game.Action{
			{Kind: game.ActChi, Seat: 0, Tile: tile.Tile{Kind: tile.Pin3},
				Consumed: []tile.Tile{{Kind: tile.Pin4}, {Kind: tile.Pin5}}},
			{Kind: game.ActPass, Seat: 0},
		},
	}
	if got := decideResponseAction(obs, StyleClosed); got.Kind != game.ActPass {
		t.Fatalf("门清打风应 pass，实际 %v", got.Kind)
	}
}

// TestDecideResponsePassesWhenOpponentRiichi 有人立直时不鸣。
func TestDecideResponsePassesWhenOpponentRiichi(t *testing.T) {
	obs := game.Observation{
		Seat:   0,
		Hand:   testHand(t, "123m 456m 789m 45p 7p 9p"),
		Riichi: [4]bool{false, true, false, false},
		Responses: []game.Action{
			{Kind: game.ActChi, Seat: 0, Tile: tile.Tile{Kind: tile.Pin3},
				Consumed: []tile.Tile{{Kind: tile.Pin4}, {Kind: tile.Pin5}}},
			{Kind: game.ActPass, Seat: 0},
		},
	}
	if got := decideResponseAction(obs, StyleAttack); got.Kind != game.ActPass {
		t.Fatalf("对手立直时应 pass，实际 %v", got.Kind)
	}
}
