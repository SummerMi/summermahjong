package agent

import (
	"testing"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// TestMaybeRiichiPrefersRiichiWhenLegal 同一切牌有立直选项时返回立直。
func TestMaybeRiichiPrefersRiichiWhenLegal(t *testing.T) {
	tl := tile.Tile{Kind: tile.Pin5}
	obs := game.Observation{
		Seat: 0,
		LegalActions: []game.Action{
			{Kind: game.ActDiscard, Seat: 0, Tile: tl},
			{Kind: game.ActRiichi, Seat: 0, Tile: tl},
		},
	}
	got := maybeRiichi(obs, tl)
	if got.Kind != game.ActRiichi {
		t.Fatalf("有立直选项时应宣言立直，实际 %v", got.Kind)
	}
}

// TestMaybeRiichiFallsBackToDiscard 没有立直选项时保持打牌。
func TestMaybeRiichiFallsBackToDiscard(t *testing.T) {
	tl := tile.Tile{Kind: tile.Pin5}
	obs := game.Observation{
		Seat:         0,
		LegalActions: []game.Action{{Kind: game.ActDiscard, Seat: 0, Tile: tl}},
	}
	got := maybeRiichi(obs, tl)
	if got.Kind != game.ActDiscard || got.Tile != tl {
		t.Fatalf("没有立直选项时应打牌，实际 %+v", got)
	}
}

// TestMaybeRiichiOnlyMatchesSameTile 立直选项只对同一切牌生效。
func TestMaybeRiichiOnlyMatchesSameTile(t *testing.T) {
	obs := game.Observation{
		Seat: 0,
		LegalActions: []game.Action{
			{Kind: game.ActRiichi, Seat: 0, Tile: tile.Tile{Kind: tile.Man1}},
		},
	}
	got := maybeRiichi(obs, tile.Tile{Kind: tile.Pin5})
	if got.Kind != game.ActDiscard {
		t.Fatalf("切牌与立直牌不同时应打牌，实际 %+v", got)
	}
}
