// Package storetest 是 store.Store 的契约测试套件。
//
// memory / sql / redis 三种实现都必须跑同一套测试（契约 §5 要求行为一致）：
//
//	func TestStore(t *testing.T) { storetest.Run(t, func() store.Store { return store.NewMemory() }) }
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
)

// Run 对给定工厂产出的 Store 实现跑全部契约测试。
func Run(t *testing.T, factory func() store.Store) {
	t.Helper()

	newMeta := func(id string) store.GameMeta {
		return store.GameMeta{
			ID:        id,
			Seed:      42,
			Rules:     game.DefaultRuleset(),
			Agents:    []string{"random", "random", "random", "random"},
			CreatedAt: time.Unix(0, 0).UTC(),
		}
	}

	t.Run("CreateGameIsIdempotent", func(t *testing.T) {
		ctx := context.Background()
		s := factory()
		meta := newMeta("g1")
		if err := s.CreateGame(ctx, meta); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := s.CreateGame(ctx, meta); err != nil {
			t.Fatalf("create twice should be idempotent, got %v", err)
		}
		got, err := s.GetMeta(ctx, "g1")
		if err != nil {
			t.Fatalf("get meta: %v", err)
		}
		if got.Seed != meta.Seed || got.ID != meta.ID {
			t.Fatalf("meta mismatch: %+v", got)
		}
	})

	t.Run("GetMetaUnknown", func(t *testing.T) {
		if _, err := factory().GetMeta(context.Background(), "nope"); !errors.Is(err, store.ErrGameNotFound) {
			t.Fatalf("want ErrGameNotFound, got %v", err)
		}
	})

	t.Run("AppendAndLoadFromSeq", func(t *testing.T) {
		ctx := context.Background()
		s := factory()
		if err := s.CreateGame(ctx, newMeta("g2")); err != nil {
			t.Fatal(err)
		}
		events := []game.Event{
			{Seq: 1, Kind: game.EventRoundStart, Seat: 0},
			{Seq: 2, Kind: game.EventDraw, Seat: 0, Tile: "5m"},
			{Seq: 3, Kind: game.EventDiscard, Seat: 0, Tile: "5m"},
		}
		if err := s.AppendEvents(ctx, "g2", events); err != nil {
			t.Fatalf("append: %v", err)
		}
		got, err := s.LoadEvents(ctx, "g2", 0)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("loaded %d events want 3", len(got))
		}
		got, err = s.LoadEvents(ctx, "g2", 2)
		if err != nil {
			t.Fatalf("load from 2: %v", err)
		}
		if len(got) != 1 || got[0].Seq != 3 {
			t.Fatalf("load from seq 2 = %+v", got)
		}
	})

	t.Run("AppendUnknownGame", func(t *testing.T) {
		err := factory().AppendEvents(context.Background(), "ghost", []game.Event{{Seq: 1}})
		if !errors.Is(err, store.ErrGameNotFound) {
			t.Fatalf("want ErrGameNotFound, got %v", err)
		}
	})

	t.Run("SnapshotRoundTrip", func(t *testing.T) {
		ctx := context.Background()
		s := factory()
		if err := s.CreateGame(ctx, newMeta("g3")); err != nil {
			t.Fatal(err)
		}
		if _, _, ok, _ := s.LoadSnapshot(ctx, "g3"); ok {
			t.Fatal("snapshot should be absent initially")
		}
		snap := []byte(`{"seq":10}`)
		if err := s.SaveSnapshot(ctx, "g3", 10, snap); err != nil {
			t.Fatalf("save snapshot: %v", err)
		}
		got, at, ok, err := s.LoadSnapshot(ctx, "g3")
		if err != nil || !ok {
			t.Fatalf("load snapshot: err=%v ok=%v", err, ok)
		}
		if at != 10 || string(got) != string(snap) {
			t.Fatalf("snapshot mismatch at=%d body=%s", at, got)
		}
	})
}
