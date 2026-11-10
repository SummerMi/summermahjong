package game

import (
	"testing"

	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// TestRulesScorerErrors RulesScorer 的错误分支（无法拆解 / 荣和缺放铳者）。
func TestRulesScorerErrors(t *testing.T) {
	scorer := RulesScorer{}
	// 不成和的手牌
	_, err := scorer.ScoreWin(WinContext{
		Seat: 0, DealerSeat: 0, RonTarget: 1,
		Hand:        mustHand(t, "1m4m7m1s4s7s2p5p8p1z2z3z9s9m"),
		WinningTile: mustTile(t, "9m"),
		RoundWind:   East, SeatWind: East,
	})
	if err == nil {
		t.Fatal("不成和手牌应返回错误")
	}
	// 荣和缺放铳者
	if _, err := scorer.ScoreWin(WinContext{
		Seat: 1, DealerSeat: 0, RonTarget: 7,
		Hand:        mustHand(t, "123m 456m 789p 11p 456s"),
		WinningTile: mustTile(t, "6s"),
		RoundWind:   East, SeatWind: South,
	}); err == nil {
		t.Fatal("荣和缺放铳者应返回错误")
	}
	// 非法副露 Kind → 拆解失败
	if _, err := scorer.ScoreWin(WinContext{
		Seat: 1, DealerSeat: 0, RonTarget: 0,
		Hand:        mustHand(t, "123m456m789m11p"),
		Melds:       []Meld{{Kind: MeldKind("bogus"), Tiles: []tile.Tile{{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East}}}},
		WinningTile: mustTile(t, "1p"),
		RoundWind:   East, SeatWind: South,
	}); err == nil {
		t.Fatal("非法副露 Kind 应返回错误")
	}
}

// TestBuildScoreContextWinds 覆盖自风/场风映射（北风走 default 分支）与门清判定。
func TestBuildScoreContextWinds(t *testing.T) {
	shape := rules.WinShape{Pattern: rules.PatternSevenPairs, Wait: rules.WaitTanki}
	ctx := buildScoreContext(shape, WinContext{
		SeatWind:  North,
		RoundWind: West,
		Honba:     2,
	})
	if ctx.SeatWind != tile.North {
		t.Fatalf("SeatWind=%v want 北", ctx.SeatWind)
	}
	if ctx.RoundWind != tile.West {
		t.Fatalf("RoundWind=%v want 西", ctx.RoundWind)
	}
	if ctx.Honba != 2 {
		t.Fatalf("Honba=%d want 2", ctx.Honba)
	}
	if !ctx.Menzen {
		t.Fatal("七对形（无 Open 面子）应判门清")
	}
}

// TestWinDeltasTsumoHonba 自摸本场加成：每家额外 100/本，和了者总收 Total。
func TestWinDeltasTsumoHonba(t *testing.T) {
	// 子家自摸 1 翻 30 符（base 240）：子付 300、亲付 500；2 本场每家 +200
	points := rules.Score(1, 30, rules.ScoreContext{
		Menzen: true, Tsumo: true, SeatWind: tile.South, RoundWind: tile.East, Honba: 2,
	})
	deltas, err := winDeltas(points, WinContext{
		Seat: 1, DealerSeat: 0, Tsumo: true, Dealer: false, Honba: 2, SeatWind: South,
	})
	if err != nil {
		t.Fatalf("winDeltas: %v", err)
	}
	sum := 0
	for _, delta := range deltas {
		sum += delta
	}
	if sum != 0 {
		t.Fatalf("deltas 总和=%d want 0（%v）", sum, deltas)
	}
	if deltas[1] != points.Total {
		t.Fatalf("和了者=%d want Total=%d", deltas[1], points.Total)
	}
	if deltas[0] != -(points.TsumoDealer+200) || deltas[2] != -(points.TsumoChild+200) {
		t.Fatalf("自摸分摊=%v（child=%d dealer=%d）", deltas, points.TsumoChild, points.TsumoDealer)
	}
}

// TestRulesScorerMeldedDirectConsistent 副露和牌：引擎结算 == rules 原生副露拆解直算。
func TestRulesScorerMeldedDirectConsistent(t *testing.T) {
	hand := mustHand(t, "123m456m789m11p")
	winning := mustTile(t, "1p")
	melds := []Meld{{Kind: MeldPon, Tiles: []tile.Tile{
		{Kind: tile.East}, {Kind: tile.East}, {Kind: tile.East},
	}, From: 2}}

	rm, ok := toRulesMelds(melds)
	if !ok {
		t.Fatal("toRulesMelds 失败")
	}
	shape, ok := rules.Decompose(hand, rm, winning)
	if !ok {
		t.Fatal("rules.Decompose(副露) 应判和")
	}
	ctx := rules.ScoreContext{Menzen: false, SeatWind: tile.South, RoundWind: tile.East}
	_, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	ctx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, ctx)
	points := rules.Score(han+dora, fu, ctx)

	winCtx := WinContext{
		Seat: 1, DealerSeat: 0, RonTarget: 0,
		RoundWind: East, SeatWind: South,
		Hand: hand, Melds: melds, WinningTile: winning,
	}
	result, err := (RulesScorer{}).ScoreWin(winCtx)
	if err != nil {
		t.Fatalf("RulesScorer: %v", err)
	}
	if result.Han != han+dora || result.Fu != fu {
		t.Fatalf("RulesScorer han/fu=%d/%d want %d/%d", result.Han, result.Fu, han+dora, fu)
	}
	want := rulesDirectDeltas(points, 1, 0, false, 0, 0)
	if result.Deltas != want {
		t.Fatalf("RulesScorer deltas=%v want %v", result.Deltas, want)
	}
}

// TestRulesScorerAnkanDirectConsistent 暗杠和牌：暗杠不破门清，符数按暗杠计。
func TestRulesScorerAnkanDirectConsistent(t *testing.T) {
	hand := mustHand(t, "123m456m789m11p")
	winning := mustTile(t, "1p")
	melds := []Meld{{Kind: MeldKanAnkan, Tiles: []tile.Tile{
		{Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5}, {Kind: tile.Pin5},
	}, From: 1}}

	rm, ok := toRulesMelds(melds)
	if !ok {
		t.Fatal("toRulesMelds 失败")
	}
	shape, ok := rules.Decompose(hand, rm, winning)
	if !ok {
		t.Fatal("rules.Decompose(暗杠) 应判和")
	}
	if !shapeMenzen(shape) {
		t.Fatal("暗杠不应破坏门清")
	}
	ctx := rules.ScoreContext{Menzen: true, SeatWind: tile.South, RoundWind: tile.East}
	_, han, fu, err := rules.Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	// 门清 + 单骑 + 暗杠 5p：20 + 10(门清荣和? 此处荣和) ... 用 rules 返回值直接对比即可
	points := rules.Score(han, fu, ctx)

	winCtx := WinContext{
		Seat: 1, DealerSeat: 0, RonTarget: 0,
		RoundWind: East, SeatWind: South,
		Hand: hand, Melds: melds, WinningTile: winning,
	}
	result, err := (RulesScorer{}).ScoreWin(winCtx)
	if err != nil {
		t.Fatalf("RulesScorer: %v", err)
	}
	if result.Han != han || result.Fu != fu {
		t.Fatalf("RulesScorer han/fu=%d/%d want %d/%d", result.Han, result.Fu, han, fu)
	}
	want := rulesDirectDeltas(points, 1, 0, false, 0, 0)
	if result.Deltas != want {
		t.Fatalf("RulesScorer deltas=%v want %v", result.Deltas, want)
	}
}
