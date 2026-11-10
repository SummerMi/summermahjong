package llm

import (
	"testing"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

func TestTopCandidatesLimitAndFields(t *testing.T) {
	obs := makeObs(t, defaultHand14)
	candidates := TopCandidates(obs, 3)
	if len(candidates) != 3 {
		t.Fatalf("len = %d, want 3", len(candidates))
	}
	for i, candidate := range candidates {
		if candidate.Tile == "" {
			t.Fatalf("candidate %d has empty tile", i)
		}
		if candidate.Shanten < 0 {
			t.Fatalf("candidate %+v has negative shanten", candidate)
		}
		if candidate.Ukeire < 0 || candidate.Danger < 0 || candidate.Potential < 0 {
			t.Fatalf("candidate %+v has negative stats", candidate)
		}
	}
	for i := 1; i < len(candidates); i++ {
		if candidates[i-1].Score < candidates[i].Score {
			t.Fatalf("candidates not sorted by score: %+v", candidates)
		}
	}

	// 打出 4p 后是 123m456m789m11p23p 的 1p/4p 听牌；取全部候选（k 足够大）时应在列表里。
	all := TopCandidates(obs, 20)
	best := findCandidate(all, "4p")
	if best == nil {
		t.Fatalf("4p should be a candidate: %+v", all)
	}
	if best.Shanten != 0 {
		t.Fatalf("discard 4p shanten = %d, want 0", best.Shanten)
	}
	if best.Potential == 0 {
		t.Fatalf("discard 4p potential should be > 0: %+v", best)
	}
	if best.Ukeire != 5 { // 1p 剩 2 + 4p 剩 3（手里已见 1 张）
		t.Fatalf("discard 4p ukeire = %d, want 5", best.Ukeire)
	}
}

func TestTopCandidatesEdgeCases(t *testing.T) {
	if got := TopCandidates(makeObs(t, defaultHand14), 0); got != nil {
		t.Fatalf("TopCandidates(k=0) = %v, want nil", got)
	}
	obs := makeObs(t, defaultHand14)
	obs.LegalActions = []game.Action{{Kind: game.ActTsumo, Seat: obs.Seat}}
	if got := TopCandidates(obs, 5); got != nil {
		t.Fatalf("TopCandidates without discards = %v, want nil", got)
	}
}

func TestDangerUsesRiichiGenbutsuAndSuji(t *testing.T) {
	obs := makeObs(t, defaultHand14, func(o *game.Observation) {
		o.Riichi = [4]bool{false, true, false, false}
		o.Discards[1] = mustTiles(t, "1m", "1p")
	})
	candidates := TopCandidates(obs, 20)
	genbutsu := findCandidate(candidates, "1p") // 对手打过 1p → 现物
	if genbutsu == nil {
		t.Fatalf("1p candidate missing: %+v", candidates)
	}
	if genbutsu.Danger != 0 {
		t.Fatalf("genbutsu danger = %v, want 0", genbutsu.Danger)
	}
	suji := findCandidate(candidates, "4m")  // 1m 打过 → 4m 是筋
	plain := findCandidate(candidates, "5m") // 5m 没有筋信号
	if suji == nil || plain == nil {
		t.Fatalf("4m/5m candidates missing: %+v", candidates)
	}
	if suji.Danger >= plain.Danger {
		t.Fatalf("suji 4m danger %.1f should be lower than 5m danger %.1f", suji.Danger, plain.Danger)
	}
}

func TestDangerWithoutRiichiIsZero(t *testing.T) {
	obs := makeObs(t, defaultHand14)
	candidates := TopCandidates(obs, 20)
	for _, candidate := range candidates {
		if candidate.Danger != 0 {
			t.Fatalf("no riichi opponent should mean danger 0: %+v", candidate)
		}
	}
}

func TestPotentialIsZeroWithOpenMelds(t *testing.T) {
	hand := mustTiles(t, "1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p")
	obs := game.Observation{
		Seat:          0,
		Hand:          hand,
		WallRemaining: 70,
		Melds: [4][]game.Meld{
			{{Kind: game.MeldPon, Tiles: mustTiles(t, "2p", "2p", "2p")}},
		},
	}
	for _, handTile := range hand {
		obs.LegalActions = append(obs.LegalActions, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: handTile})
	}
	candidates := TopCandidates(obs, 5)
	if len(candidates) == 0 {
		t.Fatalf("expected candidates for open hand")
	}
	for _, candidate := range candidates {
		if candidate.Potential != 0 {
			t.Fatalf("open hand potential should be 0 (Decompose 只支持门清): %+v", candidate)
		}
	}
}

func TestMaxPotentialHanClosedHand(t *testing.T) {
	hand := mustTiles(t, "1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p")
	obs := makeObs(t, defaultHand14)
	if got := maxPotentialHan(obs, hand, 0); got < 1 {
		t.Fatalf("maxPotentialHan(closed tenpai) = %d, want >= 1", got)
	}
	if got := maxPotentialHan(obs, hand, 1); got != 0 {
		t.Fatalf("maxPotentialHan(open) = %d, want 0", got)
	}
}

func TestVisibleCountsIncludesAllZones(t *testing.T) {
	obs := makeObs(t, defaultHand14, func(o *game.Observation) {
		o.Discards[2] = mustTiles(t, "1p", "9s")
		o.Melds[3] = []game.Meld{{Kind: game.MeldPon, Tiles: mustTiles(t, "E", "E", "E")}}
		o.DoraIndicators = mustTiles(t, "3p")
	})
	visible := visibleCountsOf(obs)
	if visible[tile.Pin1] != 3 { // 手牌 1p×2 + seat2 舍牌 1p×1
		t.Fatalf("visible 1p = %d, want 3", visible[tile.Pin1])
	}
	if visible[tile.Sou9] != 1 {
		t.Fatalf("visible 9s = %d, want 1", visible[tile.Sou9])
	}
	if visible[tile.East] != 3 {
		t.Fatalf("visible E = %d, want 3", visible[tile.East])
	}
	if visible[tile.Pin3] != 2 { // 手牌 3p×1（defaultHand14 含 3p）+ 宝牌指示牌 3p×1
		t.Fatalf("visible 3p = %d, want 2", visible[tile.Pin3])
	}
}

func TestUkeireAfterTenpai(t *testing.T) {
	obs := makeObs(t, defaultHand14)
	visible := visibleCountsOf(obs)
	// 打出 4p 后手牌：123m 456m 789m 11p 23p，听 1p（剩 2）与 4p（剩余 4-1=3）。
	after := mustTiles(t, "1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p")
	if got := ukeireAfter(after, 0, 0, visible); got != 5 {
		t.Fatalf("ukeire = %d, want 5", got)
	}
}

func TestMeldCountOf(t *testing.T) {
	obs := makeObs(t, defaultHand14)
	if got := meldCountOf(obs); got != 0 {
		t.Fatalf("meldCountOf = %d, want 0", got)
	}
	obs.Melds[0] = []game.Meld{{Kind: game.MeldPon}}
	if got := meldCountOf(obs); got != 1 {
		t.Fatalf("meldCountOf = %d, want 1", got)
	}
}
