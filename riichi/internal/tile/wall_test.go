package tile

import (
	"errors"
	"testing"
)

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
		aka := 0
		akaKinds := map[Kind]bool{}
		for _, tl := range deck {
			if tl.Aka {
				aka++
				akaKinds[tl.Kind] = true
				if !tl.Kind.IsFive() {
					t.Fatalf("aka tile on non-five kind %s", tl.Kind)
				}
			}
		}
		if aka != redFives {
			t.Fatalf("aka count=%d want %d", aka, redFives)
		}
		if len(akaKinds) != redFives {
			t.Fatalf("aka kinds=%d want %d", len(akaKinds), redFives)
		}
	}
}

// TestBuildDeckRedOrder 固定赤宝牌的标记顺序：5m、5p、5s 依次，且每种只有一张。
func TestBuildDeckRedOrder(t *testing.T) {
	wantKinds := [][]Kind{
		{},
		{Man5},
		{Man5, Pin5},
		{Man5, Pin5, Sou5},
	}
	for redFives, want := range wantKinds {
		deck, err := BuildDeck(redFives)
		if err != nil {
			t.Fatalf("BuildDeck(%d) error: %v", redFives, err)
		}
		var got []Kind
		for _, tl := range deck {
			if tl.Aka {
				got = append(got, tl.Kind)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("BuildDeck(%d) aka kinds=%v want %v", redFives, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("BuildDeck(%d) aka kinds=%v want %v", redFives, got, want)
			}
		}
	}
}

func TestBuildDeckInvalidRedCount(t *testing.T) {
	for _, n := range []int{-1, 4, 9} {
		if _, err := BuildDeck(n); err == nil {
			t.Fatalf("BuildDeck(%d) should fail", n)
		} else if !errors.Is(err, ErrInvalidRedCount) {
			t.Fatalf("BuildDeck(%d) error=%v want ErrInvalidRedCount", n, err)
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

// TestNewWallInvalidRedFives 覆盖 NewWall 的赤宝牌数量校验错误分支。
func TestNewWallInvalidRedFives(t *testing.T) {
	for _, n := range []int{-1, 4} {
		w, err := NewWall(1, n)
		if err == nil {
			t.Fatalf("NewWall(1, %d) should fail", n)
		}
		if w != nil {
			t.Fatalf("NewWall(1, %d) returned non-nil wall on error", n)
		}
		if err != nil && !errors.Is(err, ErrInvalidRedCount) {
			t.Fatalf("NewWall(1, %d) error=%v want ErrInvalidRedCount", n, err)
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

// TestDealInsufficientTiles 覆盖 Deal 的牌墙不足错误分支（发两轮后第三轮不够）。
func TestDealInsufficientTiles(t *testing.T) {
	w, err := NewWall(7, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	if _, err := w.Deal(); err != nil {
		t.Fatalf("first Deal error: %v", err)
	}
	if _, err := w.Deal(); err != nil {
		t.Fatalf("second Deal error: %v", err)
	}
	if got := w.Remaining(); got != LiveWallSize-2*(4*InitialHandSize) {
		t.Fatalf("remaining=%d want %d", got, LiveWallSize-2*(4*InitialHandSize))
	}
	if _, err := w.Deal(); err == nil {
		t.Fatal("third Deal should fail: not enough live tiles")
	}
}

// TestDrawRinshanBoundary 覆盖岭上牌的 4 张上限与取牌顺序。
func TestDrawRinshanBoundary(t *testing.T) {
	w, err := NewWall(2026, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	dead := w.Dead()
	if got := w.KanCount(); got != 0 {
		t.Fatalf("initial kan count=%d want 0", got)
	}
	for i := 0; i < RinshanCount; i++ {
		tile, ok := w.DrawRinshan()
		if !ok {
			t.Fatalf("rinshan %d should succeed", i)
		}
		if tile != dead[i] {
			t.Fatalf("rinshan %d tile=%v want dead[%d]=%v", i, tile, i, dead[i])
		}
		if got := w.KanCount(); got != i+1 {
			t.Fatalf("after %d rinshan kan count=%d want %d", i+1, got, i+1)
		}
	}
	if _, ok := w.DrawRinshan(); ok {
		t.Fatalf("rinshan %d should fail (cap %d)", RinshanCount+1, RinshanCount)
	}
	if got := w.KanCount(); got != RinshanCount {
		t.Fatalf("kan count after overflowing draw=%d want %d", got, RinshanCount)
	}
	// 摸岭上牌不消耗活牌。
	if got := w.Remaining(); got != LiveWallSize {
		t.Fatalf("remaining after rinshan=%d want %d", got, LiveWallSize)
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

// TestDoraIndicatorsContentAndCopy 覆盖宝牌指示牌的区段来源与「返回副本」语义。
func TestDoraIndicatorsContentAndCopy(t *testing.T) {
	w, err := NewWall(31415, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	dead := w.Dead()
	initial := w.DoraIndicators()
	if len(initial) != 1 {
		t.Fatalf("initial dora indicators=%d want 1", len(initial))
	}
	if initial[0] != dead[RinshanCount] {
		t.Fatalf("initial dora=%v want dead[%d]=%v", initial[0], RinshanCount, dead[RinshanCount])
	}

	// 修改返回值不应污染牌山内部状态。
	initial[0] = Tile{Kind: Chun}
	if got := w.DoraIndicators()[0]; got != dead[RinshanCount] {
		t.Fatalf("DoraIndicators returned aliased slice: got %v want %v", got, dead[RinshanCount])
	}

	for i := 0; i < RinshanCount; i++ {
		if _, ok := w.DrawRinshan(); !ok {
			t.Fatalf("rinshan %d failed", i)
		}
	}
	all := w.DoraIndicators()
	want := dead[RinshanCount : RinshanCount+DoraIndicatorCap]
	if len(all) != DoraIndicatorCap {
		t.Fatalf("dora indicators=%d want %d", len(all), DoraIndicatorCap)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Fatalf("dora[%d]=%v want %v", i, all[i], want[i])
		}
	}
}

// TestDoraIndicatorsClamp 白盒覆盖防御性上限：即使内部计数异常也不越界、不超 5 张。
func TestDoraIndicatorsClamp(t *testing.T) {
	w, err := NewWall(9, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	w.rinshan = DoraIndicatorCap + 2
	if got := len(w.DoraIndicators()); got != DoraIndicatorCap {
		t.Fatalf("dora indicators=%d want clamp to %d", got, DoraIndicatorCap)
	}
	if _, ok := w.DrawRinshan(); ok {
		t.Fatal("DrawRinshan should fail when rinshan counter is over the cap")
	}
}

// TestUraIndicatorsContentAndCopy 覆盖裏宝牌指示牌的区段来源与「返回副本」语义。
func TestUraIndicatorsContentAndCopy(t *testing.T) {
	w, err := NewWall(2718, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	dead := w.Dead()
	ura := w.UraIndicators()
	if len(ura) != DoraIndicatorCap {
		t.Fatalf("ura indicators=%d want %d", len(ura), DoraIndicatorCap)
	}
	want := dead[RinshanCount+DoraIndicatorCap:]
	if len(want) != DoraIndicatorCap {
		t.Fatalf("dead wall layout broken: %d", len(want))
	}
	for i := range want {
		if ura[i] != want[i] {
			t.Fatalf("ura[%d]=%v want %v", i, ura[i], want[i])
		}
	}
	ura[0] = Tile{Kind: Chun}
	if got := w.UraIndicators()[0]; got != want[0] {
		t.Fatalf("UraIndicators returned aliased slice: got %v want %v", got, want[0])
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

// TestLiveDeadAreCopies 覆盖 Live / Dead 返回副本的语义（测试与回放依赖）。
func TestLiveDeadAreCopies(t *testing.T) {
	w, err := NewWall(11, 3)
	if err != nil {
		t.Fatalf("NewWall error: %v", err)
	}
	originalLive := w.Live()[0]
	originalDead := w.Dead()[0]
	live, dead := w.Live(), w.Dead()
	live[0] = Tile{Kind: Chun, Aka: true}
	dead[0] = Tile{Kind: Chun, Aka: true}
	if got := w.Live()[0]; got != originalLive {
		t.Fatalf("Live returned aliased slice: got %v want %v", got, originalLive)
	}
	if got := w.Dead()[0]; got != originalDead {
		t.Fatalf("Dead returned aliased slice: got %v want %v", got, originalDead)
	}
}
