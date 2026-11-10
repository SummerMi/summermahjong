package llm

import (
	"strings"
	"testing"

	"summermahjong/riichi/internal/game"
)

func TestBuildPromptContainsContextAndCandidates(t *testing.T) {
	hand := mustTiles(t, defaultHand14...)
	drawn := hand[len(hand)-1]
	obs := makeObs(t, defaultHand14, func(o *game.Observation) {
		o.Drawn = &drawn
		o.Discards[0] = mustTiles(t, "9m")
		o.Riichi[1] = true
		o.Honba = 2
	})
	candidates := []Candidate{
		{Tile: "4p", Shanten: 0, Ukeire: 3, Danger: 0, Potential: 2, Score: 7},
		{Tile: "1p", Shanten: 0, Ukeire: 2, Danger: 5, Potential: 2, Score: 1},
	}
	prompt := BuildPrompt(obs, candidates)
	for _, want := range []string{
		"牌局上下文", "candidates", `"drawn": "4p"`, `"honba": 2`,
		"CANDIDATES: [\"4p\",\"1p\"]", "请只输出一张牌的编码",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if got := firstCandidateFromPrompt(prompt); got != "4p" {
		t.Fatalf("firstCandidateFromPrompt = %q, want 4p", got)
	}
}

func TestBuildPromptWithMeldsAndDiscards(t *testing.T) {
	obs := makeObs(t, defaultHand14, func(o *game.Observation) {
		o.Melds[0] = []game.Meld{{Kind: game.MeldPon, Tiles: mustTiles(t, "E", "E", "E")}}
		o.Discards[3] = mustTiles(t, "1s", "2s")
	})
	prompt := BuildPrompt(obs, []Candidate{{Tile: "4p"}})
	for _, want := range []string{`"melds"`, `"E"`, `"discards"`, `"2s"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestFirstCandidateFromPromptEdgeCases(t *testing.T) {
	if got := firstCandidateFromPrompt("no marker here"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := firstCandidateFromPrompt("CANDIDATES: not-json"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := firstCandidateFromPrompt("CANDIDATES: []"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if got := firstCandidateFromPrompt("CANDIDATES: [\"3p\"]"); got != "3p" {
		t.Fatalf("got %q, want 3p", got)
	}
}

func TestParseTilesFromText(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"3p", []string{"3p"}},
		{"我选 r5m。", []string{"r5m"}},
		{`{"tile":"E"}`, []string{"E"}},
		{"choose 4s but not 9q", []string{"4s"}},
		{"abc no tiles", nil},
		{"3p 或 7s", []string{"3p", "7s"}},
		{"0m", []string{"r5m"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.text, func(t *testing.T) {
			got := parseTilesFromText(testCase.text)
			if len(got) != len(testCase.want) {
				t.Fatalf("parseTilesFromText(%q) = %v, want %v", testCase.text, got, testCase.want)
			}
			for i := range got {
				if got[i].String() != testCase.want[i] {
					t.Fatalf("parseTilesFromText(%q)[%d] = %s, want %s", testCase.text, i, got[i], testCase.want[i])
				}
			}
		})
	}
}

func TestPickAction(t *testing.T) {
	obs := makeObs(t, defaultHand14)
	if action, ok := pickAction(obs, "我选择 4p"); !ok || action.Tile.String() != "4p" {
		t.Fatalf("pickAction = %+v, %v", action, ok)
	}
	if _, ok := pickAction(obs, "9s"); ok {
		t.Fatalf("9s is not in hand, should be rejected")
	}
	if _, ok := pickAction(obs, "我不会"); ok {
		t.Fatalf("text without tile should be rejected")
	}
}
