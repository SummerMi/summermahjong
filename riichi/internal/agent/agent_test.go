package agent_test

import (
	"context"
	"errors"
	"testing"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

func observationWith(t *testing.T, actions []game.Action) game.Observation {
	t.Helper()
	return game.Observation{
		Seat:         0,
		Hand:         []tile.Tile{{Kind: tile.Man1}, {Kind: tile.Man2}},
		LegalActions: actions,
	}
}

func TestRandomPicksLegalAction(t *testing.T) {
	ctx := context.Background()
	obs := observationWith(t, []game.Action{
		{Kind: game.ActDraw, Seat: 0},
	})
	r := agent.NewRandom(1)
	got, err := r.Decide(ctx, obs)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if got.Kind != game.ActDraw {
		t.Fatalf("got %+v", got)
	}
}

func TestRandomIsDeterministic(t *testing.T) {
	actions := []game.Action{
		{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man1}},
		{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man2}},
	}
	obs := observationWith(t, actions)
	a := agent.NewRandom(99)
	b := agent.NewRandom(99)
	for i := 0; i < 20; i++ {
		x, _ := a.Decide(context.Background(), obs)
		y, _ := b.Decide(context.Background(), obs)
		if x.Kind != y.Kind || x.Tile != y.Tile {
			t.Fatalf("decision %d differs: %+v vs %+v", i, x, y)
		}
	}
}

func TestNoLegalAction(t *testing.T) {
	obs := observationWith(t, nil)
	if _, err := agent.NewRandom(1).Decide(context.Background(), obs); !errors.Is(err, agent.ErrNoLegalAction) {
		t.Fatalf("want ErrNoLegalAction, got %v", err)
	}
	if _, err := (agent.FirstLegal{}).Decide(context.Background(), obs); !errors.Is(err, agent.ErrNoLegalAction) {
		t.Fatalf("want ErrNoLegalAction, got %v", err)
	}
}

func TestInLegalActions(t *testing.T) {
	discardMan1 := game.Action{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man1}}
	obs := observationWith(t, []game.Action{discardMan1})

	if !agent.InLegalActions(obs, discardMan1) {
		t.Fatal("known discard should be legal")
	}
	if agent.InLegalActions(obs, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man9}}) {
		t.Fatal("unknown discard should be illegal")
	}
	if !agent.InLegalActions(obs, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: discardMan1.Tile}) {
		t.Fatal("same tile discard should be legal")
	}
	if agent.InLegalActions(obs, game.Action{Kind: game.ActPon, Seat: 0}) {
		t.Fatal("pon not in legal set")
	}
}
