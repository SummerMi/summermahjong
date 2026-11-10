package tile

import (
	"errors"
	"strings"
	"testing"
)

func TestKindRoundTrip(t *testing.T) {
	for kind := Kind(0); kind < NumKinds; kind++ {
		text := kind.String()
		parsed, err := ParseKind(text)
		if err != nil {
			t.Fatalf("ParseKind(%q) error: %v", text, err)
		}
		if parsed != kind {
			t.Fatalf("round trip mismatch: kind=%d text=%q parsed=%d", kind, text, parsed)
		}
	}
}

func TestKindProperties(t *testing.T) {
	cases := []struct {
		kind Kind
		suit Suit
		num  int
		hon  bool
		term bool
		wind bool
		drag bool
	}{
		{Man1, Man, 1, false, true, false, false},
		{Man5, Man, 5, false, false, false, false},
		{Man9, Man, 9, false, true, false, false},
		{Pin3, Pin, 3, false, false, false, false},
		{Sou7, Sou, 7, false, false, false, false},
		{East, Honor, 0, true, false, true, false},
		{North, Honor, 0, true, false, true, false},
		{Haku, Honor, 0, true, false, false, true},
		{Hatsu, Honor, 0, true, false, false, true},
		{Chun, Honor, 0, true, false, false, true},
	}
	for _, c := range cases {
		if got := c.kind.Suit(); got != c.suit {
			t.Errorf("%s.Suit()=%v want %v", c.kind, got, c.suit)
		}
		if got := c.kind.Number(); got != c.num {
			t.Errorf("%s.Number()=%d want %d", c.kind, got, c.num)
		}
		if got := c.kind.IsHonor(); got != c.hon {
			t.Errorf("%s.IsHonor()=%v want %v", c.kind, got, c.hon)
		}
		if got := c.kind.IsTerminal(); got != c.term {
			t.Errorf("%s.IsTerminal()=%v want %v", c.kind, got, c.term)
		}
		if got := c.kind.IsWind(); got != c.wind {
			t.Errorf("%s.IsWind()=%v want %v", c.kind, got, c.wind)
		}
		if got := c.kind.IsDragon(); got != c.drag {
			t.Errorf("%s.IsDragon()=%v want %v", c.kind, got, c.drag)
		}
	}
}

// TestKindHelpers 覆盖 IsTerminalOrHonor / IsSimple 这两组聚合判断。
func TestKindHelpers(t *testing.T) {
	cases := []struct {
		kind        Kind
		termOrHonor bool
		simple      bool
		five        bool
	}{
		{Man1, true, false, false},
		{Man5, false, true, true},
		{Man9, true, false, false},
		{Pin5, false, true, true},
		{Sou5, false, true, true},
		{East, true, false, false},
		{Haku, true, false, false},
		{Chun, true, false, false},
	}
	for _, c := range cases {
		if got := c.kind.IsTerminalOrHonor(); got != c.termOrHonor {
			t.Errorf("%s.IsTerminalOrHonor()=%v want %v", c.kind, got, c.termOrHonor)
		}
		if got := c.kind.IsSimple(); got != c.simple {
			t.Errorf("%s.IsSimple()=%v want %v", c.kind, got, c.simple)
		}
		if got := c.kind.IsFive(); got != c.five {
			t.Errorf("%s.IsFive()=%v want %v", c.kind, got, c.five)
		}
	}
}

// TestKindStringAndSuitString 固定 34 种牌与 4 种花色的文本输出（契约 §3/§9）。
func TestKindStringAndSuitString(t *testing.T) {
	want := map[Kind]string{
		Man1: "1m", Man2: "2m", Man3: "3m", Man4: "4m", Man5: "5m",
		Man6: "6m", Man7: "7m", Man8: "8m", Man9: "9m",
		Pin1: "1p", Pin2: "2p", Pin3: "3p", Pin4: "4p", Pin5: "5p",
		Pin6: "6p", Pin7: "7p", Pin8: "8p", Pin9: "9p",
		Sou1: "1s", Sou2: "2s", Sou3: "3s", Sou4: "4s", Sou5: "5s",
		Sou6: "6s", Sou7: "7s", Sou8: "8s", Sou9: "9s",
		East: "E", South: "S", West: "W", North: "N",
		Haku: "P", Hatsu: "F", Chun: "C",
	}
	for kind, text := range want {
		if got := kind.String(); got != text {
			t.Errorf("%d.String()=%q want %q", kind, got, text)
		}
	}
	if got := Kind(NumKinds).String(); got != "?" {
		t.Errorf("out-of-range Kind.String()=%q want %q", got, "?")
	}

	suitWant := map[Suit]string{Man: "m", Pin: "p", Sou: "s", Honor: "z"}
	for suit, text := range suitWant {
		if got := suit.String(); got != text {
			t.Errorf("Suit(%d).String()=%q want %q", suit, got, text)
		}
	}
	if got := Suit(99).String(); got != "?" {
		t.Errorf("out-of-range Suit.String()=%q want %q", got, "?")
	}
}

func TestParseTileRed(t *testing.T) {
	cases := map[string]Tile{
		"5m":  {Kind: Man5},
		"r5m": {Kind: Man5, Aka: true},
		"0m":  {Kind: Man5, Aka: true},
		"R5p": {Kind: Pin5, Aka: true},
		"r5s": {Kind: Sou5, Aka: true},
		"5s":  {Kind: Sou5},
		"1z":  {Kind: East},
		"7z":  {Kind: Chun},
		"5z":  {Kind: Haku},
		"6z":  {Kind: Hatsu},
		"E":   {Kind: East},
		"C":   {Kind: Chun},
		"P":   {Kind: Haku},
		"F":   {Kind: Hatsu},
		"9s":  {Kind: Sou9},
	}
	for text, want := range cases {
		got, err := ParseTile(text)
		if err != nil {
			t.Fatalf("ParseTile(%q) error: %v", text, err)
		}
		if got != want {
			t.Fatalf("ParseTile(%q)=%+v want %+v", text, got, want)
		}
	}
}

func TestParseTileRedRejectsNonFive(t *testing.T) {
	for _, text := range []string{"r3m", "0p9", "rE", "0z"} {
		if _, err := ParseTile(text); err == nil {
			t.Fatalf("ParseTile(%q) should fail", text)
		}
	}
}

// TestParseTileInvalid 覆盖非法长度 / 非法花色 / 非法前缀等边界（契约 §9 之外但不允许静默放过）。
func TestParseTileInvalid(t *testing.T) {
	for _, text := range []string{
		"", " ", "r", "5", "z", "rr5m", "r5", "r5x", "r5m3",
		"1x", "mm", "12m", "0m9", "0x", "10m", "5m ", " r5m",
	} {
		_, err := ParseTile(text)
		if err == nil {
			t.Fatalf("ParseTile(%q) should fail", text)
		}
		if !errors.Is(err, ErrInvalidTile) {
			t.Fatalf("ParseTile(%q) error=%v, want ErrInvalidTile", text, err)
		}
	}
}

// TestParseKindEdge 覆盖 ParseKind 的简写/大小写/非法分支。
func TestParseKindEdge(t *testing.T) {
	cases := map[string]Kind{
		"E": East, "e": East,
		"S": South, "s": South,
		"W": West, "w": West,
		"N": North, "n": North,
		"P": Haku, "p": Haku,
		"F": Hatsu, "f": Hatsu,
		"C": Chun, "c": Chun,
		"5z": Haku, "6z": Hatsu, "7z": Chun,
		"5Z": Haku, "6h": Hatsu, "7H": Chun,
		"1z": East, "4z": North,
		"5M": Man5, "9P": Pin9, "1S": Sou1,
	}
	for text, want := range cases {
		got, err := ParseKind(text)
		if err != nil {
			t.Fatalf("ParseKind(%q) error: %v", text, err)
		}
		if got != want {
			t.Fatalf("ParseKind(%q)=%v want %v", text, got, want)
		}
	}
	for _, text := range []string{"", "1", "0", "0m", "10m", "5x", "12z", "8z", "9z", "m", "abc"} {
		if _, err := ParseKind(text); err == nil {
			t.Fatalf("ParseKind(%q) should fail", text)
		}
	}
}

func TestParseHand(t *testing.T) {
	hand, err := ParseHand("123m456p789s111z")
	if err != nil {
		t.Fatalf("ParseHand error: %v", err)
	}
	if len(hand) != 12 {
		t.Fatalf("len=%d want 12", len(hand))
	}
	if hand[0].Kind != Man1 || hand[11].Kind != East {
		t.Fatalf("unexpected parse: %v", hand)
	}

	// 花色分组之间允许空格，连写与分组等价。
	spaced, err := ParseHand("1m 2p 3s")
	if err != nil {
		t.Fatalf("ParseHand spaced error: %v", err)
	}
	if len(spaced) != 3 || spaced[0].Kind != Man1 || spaced[1].Kind != Pin2 || spaced[2].Kind != Sou3 {
		t.Fatalf("unexpected spaced parse: %v", spaced)
	}
	splitSuit, err := ParseHand("123 m")
	if err != nil {
		t.Fatalf("ParseHand split-suit error: %v", err)
	}
	if len(splitSuit) != 3 {
		t.Fatalf("len=%d want 3", len(splitSuit))
	}
}

func TestParseHandRejectsGarbage(t *testing.T) {
	// 注意："0m"（赤宝牌）只能走 ParseTile；ParseHand 面向普通牌面，遇到 0 视为非法。
	for _, text := range []string{"123", "12m3", "1x", "m123", "0m", "r5m", "123z4"} {
		if _, err := ParseHand(text); err == nil {
			t.Fatalf("ParseHand(%q) should fail", text)
		}
	}
}

func TestParseHandEmpty(t *testing.T) {
	hand, err := ParseHand("")
	if err != nil {
		t.Fatalf("ParseHand(empty) error: %v", err)
	}
	if len(hand) != 0 {
		t.Fatalf("len=%d want 0", len(hand))
	}
}

func TestTileString(t *testing.T) {
	if got := (Tile{Kind: Man5, Aka: true}).String(); got != "r5m" {
		t.Fatalf("red five string = %q", got)
	}
	if got := (Tile{Kind: Man5}).String(); got != "5m" {
		t.Fatalf("plain five string = %q", got)
	}
	if got := (Tile{Kind: Haku}).String(); got != "P" {
		t.Fatalf("haku string = %q", got)
	}
	if got := (Tile{Kind: Hatsu}).String(); got != "F" {
		t.Fatalf("hatsu string = %q", got)
	}
	if got := (Tile{Kind: Chun}).String(); got != "C" {
		t.Fatalf("chun string = %q", got)
	}
	if !strings.HasPrefix((Tile{Kind: Sou9}).String(), "9s") {
		t.Fatalf("unexpected sou string")
	}
	// 赤宝牌标记不同，牌本身就不相等（不能把红 5 当普通 5 打掉）。
	if (Tile{Kind: Man5, Aka: true}) == (Tile{Kind: Man5}) {
		t.Fatal("aka tile must not equal plain tile")
	}
}

// TestPopcount 覆盖诊断用的 popcount（保留函数也要有测试守着）。
func TestPopcount(t *testing.T) {
	cases := map[uint64]int{
		0: 0, 1: 1, 0b1011: 3, 0xFF: 8,
		^uint64(0): 64,
	}
	for input, want := range cases {
		if got := popcount(input); got != want {
			t.Fatalf("popcount(%#x)=%d want %d", input, got, want)
		}
	}
}

// TestSplitMix64Deterministic 用金标值锁住 PRNG：同 seed 在任何机器/Go 版本都应得到同一序列。
func TestSplitMix64Deterministic(t *testing.T) {
	rng := NewSplitMix64(0)
	if got := rng.Next(); got != 0xE220A8397B1DCDAF {
		t.Fatalf("seed 0 first value=%#x want 0xE220A8397B1DCDAF", got)
	}
	if got := rng.Next(); got != 0x6E789E6AA1B965F4 {
		t.Fatalf("seed 0 second value=%#x want 0x6E789E6AA1B965F4", got)
	}

	// 同 seed 两个实例序列一致。
	a, b := NewSplitMix64(42), NewSplitMix64(42)
	for i := 0; i < 8; i++ {
		if a.Next() != b.Next() {
			t.Fatalf("same seed diverged at %d", i)
		}
	}

	// Intn 边界：n <= 0 返回 0；n >= 1 结果落在 [0,n)。
	if got := NewSplitMix64(1).Intn(0); got != 0 {
		t.Fatalf("Intn(0)=%d want 0", got)
	}
	if got := NewSplitMix64(1).Intn(-5); got != 0 {
		t.Fatalf("Intn(-5)=%d want 0", got)
	}
	for n := 1; n <= 64; n++ {
		rng := NewSplitMix64(int64(n))
		for i := 0; i < 256; i++ {
			v := rng.Intn(n)
			if v < 0 || v >= n {
				t.Fatalf("Intn(%d)=%d out of range", n, v)
			}
		}
	}
}
