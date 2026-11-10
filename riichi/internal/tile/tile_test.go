package tile

import (
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
		{White, Honor, 0, true, false, false, true},
		{Green, Honor, 0, true, false, false, true},
		{Red, Honor, 0, true, false, false, true},
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

func TestParseTileRed(t *testing.T) {
	cases := map[string]Tile{
		"5m":  {Kind: Man5},
		"r5m": {Kind: Man5, Red: true},
		"0m":  {Kind: Man5, Red: true},
		"R5p": {Kind: Pin5, Red: true},
		"5s":  {Kind: Sou5},
		"1z":  {Kind: East},
		"7z":  {Kind: Red},
		"E":   {Kind: East},
		"C":   {Kind: Red},
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
}

func TestParseHandRejectsGarbage(t *testing.T) {
	for _, text := range []string{"123", "12m3", "1x", "m123"} {
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
	if got := (Tile{Kind: Man5, Red: true}).String(); got != "r5m" {
		t.Fatalf("red five string = %q", got)
	}
	if got := (Tile{Kind: White}).String(); got != "P" {
		t.Fatalf("white string = %q", got)
	}
	if !strings.HasPrefix((Tile{Kind: Sou9}).String(), "9s") {
		t.Fatalf("unexpected sou string")
	}
}
