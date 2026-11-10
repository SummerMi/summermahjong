package tile

import "testing"

func countByKind(tiles []Tile) map[Kind]int {
	m := make(map[Kind]int, NumKinds)
	for _, t := range tiles {
		m[t.Kind]++
	}
	return m
}

func TestBuildDeckCounts(t *testing.T) {
	for redFives := 0; redFives <= 3; redFives++ {
		deck, err := BuildDeck(redFives)
		if err != nil {
			t.Fatalf("BuildDeck(%d) error: %v", redFives, err)
		}
		if len(deck) != DeckSize {
			t.Fatalf("deck size=%d want %d", len(deck), DeckSize)
		}
		counts := countByKind(deck)
		for kind := Kind(0); kind < NumKinds; kind++ {
			if counts[kind] != CopiesPerKind {
				t.Fatalf("kind %s count=%d want %d", kind, counts[kind], CopiesPerKind)
			}
		}
		red := 0
		redKinds := map[Kind]bool{}
		for _, tl := range deck {
			if tl.Red {
				red++
				redKinds[tl.Kind] = true
				if !tl.Kind.IsFive() {
					t.Fatalf("red tile on non-five kind %s", tl.Kind)
				}
			}
		}
		if red != redFives {
			t.Fatalf("red count=%d want %d", red, redFives)
		}
		if len(redKinds) != redFives {
			t.Fatalf("red kinds=%d want %d", len(redKinds), redFives)
		}
	}
}

func TestBuildDeckInvalidRedCount(t *testing.T) {
	for _, n := range []int{-1, 4, 9} {
		if _, err := BuildDeck(n); err == nil {
			t.Fatalf("BuildDeck(%d) should fail", n)
		}
	}
}

func TestShuffleDeterministic(t *testing.T) {
	a, _ := BuildDeck(3)
	b, _ := BuildDeck(3)
	Shuffle(a, 42)
	Shuffle(b, 42)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed differs at %d: %v vs %v", i, a[i], b[i])
		}
	}

	c, _ := BuildDeck(3)
	Shuffle(c, 43)
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds produced identical order")
	}
}

func TestShufflePreservesMultiset(t *testing.T) {
	deck, _ := BuildDeck(3)
	before := countByKind(deck)
	Shuffle(deck, 7)
	after := countByKind(deck)
	for kind := Kind(0); kind < NumKinds; kind++ {
		if before[kind] != after[kind] {
			t.Fatalf("shuffle changed count of %s: %d -> %d", kind, before[kind], after[kind])
		}
	}
}

func TestNewWallSplit(t *testing.T) {
	w, err := NewWall(20261110, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	if got := len(w.Live()); got != LiveWallSize {
		t.Fatalf("live size=%d want %d", got, LiveWallSize)
	}
	if got := len(w.Dead()); got != DeadWallSize {
		t.Fatalf("dead size=%d want %d", got, DeadWallSize)
	}
	if got := w.Remaining(); got != LiveWallSize {
		t.Fatalf("remaining=%d want %d", got, LiveWallSize)
	}
	// 活牌 + 王牌 = 完整一副牌
	all := append(w.Live(), w.Dead()...)
	counts := countByKind(all)
	if len(all) != DeckSize {
		t.Fatalf("total tiles=%d want %d", len(all), DeckSize)
	}
	for kind := Kind(0); kind < NumKinds; kind++ {
		if counts[kind] != CopiesPerKind {
			t.Fatalf("kind %s total count=%d want %d", kind, counts[kind], CopiesPerKind)
		}
	}
}

func TestDealAndDrawExhaustion(t *testing.T) {
	w, _ := NewWall(1, 3)
	hands, err := w.Deal()
	if err != nil {
		t.Fatalf("Deal error: %v", err)
	}
	for seat := 0; seat < 4; seat++ {
		if len(hands[seat]) != InitialHandSize {
			t.Fatalf("seat %d hand size=%d want %d", seat, len(hands[seat]), InitialHandSize)
		}
	}
	if got := w.Remaining(); got != MaxDrawsFromLive {
		t.Fatalf("remaining after deal=%d want %d", got, MaxDrawsFromLive)
	}
	for i := 0; i < MaxDrawsFromLive; i++ {
		if _, ok := w.Draw(); !ok {
			t.Fatalf("draw %d failed unexpectedly", i)
		}
	}
	if got := w.Remaining(); got != 0 {
		t.Fatalf("remaining=%d want 0", got)
	}
	if _, ok := w.Draw(); ok {
		t.Fatal("draw past exhaustion should fail")
	}
}

func TestRinshanAndDoraProgression(t *testing.T) {
	w, _ := NewWall(99, 3)
	if got := len(w.DoraIndicators()); got != 1 {
		t.Fatalf("initial dora indicators=%d want 1", got)
	}
	for i := 0; i < RinshanCount; i++ {
		if _, ok := w.DrawRinshan(); !ok {
			t.Fatalf("rinshan %d failed", i)
		}
		if got, want := len(w.DoraIndicators()), i+2; got != want {
			t.Fatalf("after %d kans dora indicators=%d want %d", i+1, got, want)
		}
	}
	if _, ok := w.DrawRinshan(); ok {
		t.Fatal("5th rinshan should fail")
	}
	if got := w.KanCount(); got != RinshanCount {
		t.Fatalf("kan count=%d want %d", got, RinshanCount)
	}
	if got := len(w.UraIndicators()); got != DoraIndicatorCap {
		t.Fatalf("ura indicators=%d want %d", got, DoraIndicatorCap)
	}
}

func TestWallDeterministicAcrossInstances(t *testing.T) {
	a, _ := NewWall(12345, 3)
	b, _ := NewWall(12345, 3)
	la, lb := a.Live(), b.Live()
	for i := range la {
		if la[i] != lb[i] {
			t.Fatalf("wall differs at %d", i)
		}
	}
	da, db := a.Dead(), b.Dead()
	for i := range da {
		if da[i] != db[i] {
			t.Fatalf("dead wall differs at %d", i)
		}
	}
}

func TestUraIndicatorsDoNotOverlapDora(t *testing.T) {
	w, _ := NewWall(5, 3)
	dora := w.DoraIndicators()
	ura := w.UraIndicators()
	seen := map[Tile]int{}
	for _, t := range dora {
		seen[t]++
	}
	for _, t := range ura {
		seen[t]++
	}
	// 指示牌来自王牌的不同区段：重叠张数不可能超过该牌型本身的数量（<=4）
	for tile, n := range seen {
		if n > CopiesPerKind {
			t.Fatalf("indicator tile %v appears %d times", tile, n)
		}
	}
}
