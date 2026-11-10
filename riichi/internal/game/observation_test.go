package game

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestObserveDoesNotLeakOpponentHands 是契约 §2.5 的安全测试：
// 观察结果里只能出现「自己的手牌 + 公开信息」。
func TestObserveDoesNotLeakOpponentHands(t *testing.T) {
	s := mustNew(t, 11)

	// 手工构造一个可控局面：四家手牌各用一种互不相同的牌。
	s.Hands = [4][]tile.Tile{
		{tile.Tile{Kind: tile.Man1}, tile.Tile{Kind: tile.Man2}},
		{tile.Tile{Kind: tile.Pin1}, tile.Tile{Kind: tile.Pin2}},
		{tile.Tile{Kind: tile.Sou1}, tile.Tile{Kind: tile.Sou2}},
		{tile.Tile{Kind: tile.East}, tile.Tile{Kind: tile.South}},
	}
	s.Discards = [4][]tile.Tile{nil, nil, nil, nil}
	s.Melds = [4][]Meld{nil, nil, nil, nil}

	obs := s.Observe(0)
	if len(obs.Hand) != 2 {
		t.Fatalf("observed hand size=%d want 2", len(obs.Hand))
	}
	for _, tl := range obs.Hand {
		if tl.Kind != tile.Man1 && tl.Kind != tile.Man2 {
			t.Fatalf("observed hand leaked opponent tile: %v", tl)
		}
	}

	// 对手手牌里的牌种（Pin/Sou/East 等）不应以「手牌」形式出现。
	leaked := map[tile.Kind]bool{}
	for seat := 1; seat < 4; seat++ {
		for _, tl := range s.Hands[seat] {
			leaked[tl.Kind] = true
		}
	}
	for _, tl := range obs.Hand {
		if leaked[tl.Kind] {
			t.Fatalf("hand contains opponent tile %v", tl)
		}
	}
}

// TestObserveCopiesHand 保证 Agent 改观察结果不会污染引擎状态（防别名）。
func TestObserveCopiesHand(t *testing.T) {
	s := mustNew(t, 12)
	before := append([]tile.Tile(nil), s.Hands[0]...)

	obs := s.Observe(0)
	if len(obs.Hand) == 0 {
		t.Fatal("empty hand")
	}
	obs.Hand[0] = tile.Tile{Kind: tile.Man9}
	if s.Hands[0][0] != before[0] {
		t.Fatal("engine hand was mutated through observation")
	}
}

// TestObserveLegalActionsOnlyForCurrentSeat 保证只有当前座位能拿到合法动作。
func TestObserveLegalActionsOnlyForCurrentSeat(t *testing.T) {
	s := mustNew(t, 13) // 起家 0 号位，PhaseAwaitDraw
	if got := s.Observe(0).LegalActions; len(got) == 0 {
		t.Fatal("current seat should have legal actions")
	}
	for seat := Seat(1); seat < 4; seat++ {
		if got := s.Observe(seat).LegalActions; len(got) != 0 {
			t.Fatalf("seat %d should have no legal actions, got %d", seat, len(got))
		}
	}
}

// TestObserveDoraIndicatorsVisible 宝牌指示牌是公开信息，必须在观察结果里。
func TestObserveDoraIndicatorsVisible(t *testing.T) {
	s := mustNew(t, 14)
	obs := s.Observe(2)
	if len(obs.DoraIndicators) != 1 {
		t.Fatalf("dora indicators=%d want 1", len(obs.DoraIndicators))
	}
}
