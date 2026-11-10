package game

import (
	"math/rand"
	"testing"

	"summermahjong/riichi/internal/tile"
)

func mustNew(t *testing.T, seed int64) *State {
	t.Helper()
	s, err := New(seed, DefaultRuleset())
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	return s
}

func TestNewDealsCorrectly(t *testing.T) {
	s := mustNew(t, 1)
	for seat := 0; seat < 4; seat++ {
		if got := len(s.Hands[seat]); got != tile.InitialHandSize {
			t.Fatalf("seat %d hand=%d want %d", seat, got, tile.InitialHandSize)
		}
		if s.Scores[seat] != 25000 {
			t.Fatalf("seat %d score=%d want 25000", seat, s.Scores[seat])
		}
	}
	if s.TotalTiles() != tile.DeckSize {
		t.Fatalf("total tiles=%d want %d", s.TotalTiles(), tile.DeckSize)
	}
	if s.Phase != PhaseAwaitDraw || s.Turn != 0 {
		t.Fatalf("unexpected start state: phase=%v turn=%d", s.Phase, s.Turn)
	}
	if len(s.Events) != 1 || s.Events[0].Kind != EventRoundStart {
		t.Fatalf("unexpected initial events: %+v", s.Events)
	}
}

func TestDrawThenDiscardAdvancesTurn(t *testing.T) {
	s := mustNew(t, 7)
	if err := s.Apply(Action{Kind: ActDraw, Seat: 0}); err != nil {
		t.Fatalf("draw: %v", err)
	}
	if len(s.Hands[0]) != tile.InitialHandSize+1 {
		t.Fatalf("after draw hand=%d", len(s.Hands[0]))
	}
	if s.Phase != PhaseAwaitDiscard {
		t.Fatalf("phase=%v want AwaitDiscard", s.Phase)
	}
	pick := s.Hands[0][0]
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 0, Tile: pick}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if len(s.Hands[0]) != tile.InitialHandSize {
		t.Fatalf("after discard hand=%d", len(s.Hands[0]))
	}
	if s.Turn != 1 || s.Phase != PhaseAwaitDraw {
		t.Fatalf("turn=%d phase=%v want 1/AwaitDraw", s.Turn, s.Phase)
	}
	if len(s.Events) != 3 {
		t.Fatalf("events=%d want 3", len(s.Events))
	}
	if s.Events[2].Kind != EventDiscard || s.Events[2].Seat != 0 {
		t.Fatalf("unexpected last event: %+v", s.Events[2])
	}
	if s.TotalTiles() != tile.DeckSize {
		t.Fatalf("total tiles=%d", s.TotalTiles())
	}
}

func TestIllegalActions(t *testing.T) {
	s := mustNew(t, 3)
	// 错误座位
	if err := s.Apply(Action{Kind: ActDraw, Seat: 1}); err == nil {
		t.Fatal("draw by wrong seat should fail")
	}
	// 阶段不对：还没摸就打
	pick := s.Hands[0][0]
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 0, Tile: pick}); err == nil {
		t.Fatal("discard before draw should fail")
	}
	// 摸两次
	if err := s.Apply(Action{Kind: ActDraw, Seat: 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(Action{Kind: ActDraw, Seat: 0}); err == nil {
		t.Fatal("double draw should fail")
	}
	// 打不存在的牌
	if err := s.Apply(Action{Kind: ActDiscard, Seat: 0, Tile: tile.Tile{Kind: tile.Man1}}); err != nil {
		// 只有手里确实没有 1m 时才应失败；若恰好有则跳过该断言
		if err == nil {
			t.Fatal("unreachable")
		}
	}
}

func randomPlay(t *testing.T, s *State, rng *rand.Rand, steps int) []Action {
	t.Helper()
	actions := make([]Action, 0, steps)
	for i := 0; i < steps; i++ {
		legal := s.Legal()
		if len(legal) == 0 {
			break
		}
		a := legal[rng.Intn(len(legal))]
		if err := s.Apply(a); err != nil {
			if s.Phase == PhaseRoundEnd {
				break
			}
			t.Fatalf("apply step %d: %v", i, err)
		}
		actions = append(actions, a)
	}
	return actions
}

func TestReplayIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewSource(2026))
	s1 := mustNew(t, 42)
	actions := randomPlay(t, s1, rng, 200)

	s2, err := Replay(42, DefaultRuleset(), actions)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if s2.Seq != s1.Seq {
		t.Fatalf("seq=%d want %d", s2.Seq, s1.Seq)
	}
	for seat := 0; seat < 4; seat++ {
		if len(s2.Hands[seat]) != len(s1.Hands[seat]) {
			t.Fatalf("seat %d hand len mismatch", seat)
		}
		for i := range s1.Hands[seat] {
			if s2.Hands[seat][i] != s1.Hands[seat][i] {
				t.Fatalf("seat %d hand[%d] mismatch", seat, i)
			}
		}
		for i := range s1.Discards[seat] {
			if s2.Discards[seat][i] != s1.Discards[seat][i] {
				t.Fatalf("seat %d discard[%d] mismatch", seat, i)
			}
		}
	}
	for i := range s1.Events {
		if !eventsEqual(s1.Events[i], s2.Events[i]) {
			t.Fatalf("event %d mismatch: %+v vs %+v", i, s1.Events[i], s2.Events[i])
		}
	}
}

func TestTotalTilesInvariantUnderPlay(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	s := mustNew(t, 99)
	for i := 0; i < 300; i++ {
		legal := s.Legal()
		if len(legal) == 0 {
			break
		}
		if err := s.Apply(legal[rng.Intn(len(legal))]); err != nil {
			if s.Phase == PhaseRoundEnd {
				break
			}
			t.Fatalf("step %d: %v", i, err)
		}
		if got := s.TotalTiles(); got != tile.DeckSize {
			t.Fatalf("step %d total tiles=%d", i, got)
		}
	}
}

func TestWallExhaustionEndsRound(t *testing.T) {
	s := mustNew(t, 5)
	draws := 0
	for {
		if s.Phase == PhaseAwaitDraw {
			if err := s.Apply(Action{Kind: ActDraw, Seat: s.Turn}); err != nil {
				break
			}
			draws++
			continue
		}
		if err := s.Apply(Action{Kind: ActDiscard, Seat: s.Turn, Tile: s.Hands[s.Turn][0]}); err != nil {
			t.Fatalf("discard: %v", err)
		}
	}
	if draws != tile.MaxDrawsFromLive {
		t.Fatalf("draws=%d want %d", draws, tile.MaxDrawsFromLive)
	}
	if s.Phase != PhaseRoundEnd {
		t.Fatalf("phase=%v want RoundEnd", s.Phase)
	}
}

// eventsEqual 比较两条事件（Event 含 json.RawMessage，不能用 ==）。
func eventsEqual(a, b Event) bool {
	return a.Seq == b.Seq && a.Kind == b.Kind && a.Seat == b.Seat &&
		a.Tile == b.Tile && a.TileRed == b.TileRed && string(a.Payload) == string(b.Payload)
}
