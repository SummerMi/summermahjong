package eval

import (
	"strings"
	"testing"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

func mustHand(t *testing.T, text string) []tile.Tile {
	t.Helper()
	hand, err := tile.ParseHand(text)
	if err != nil {
		t.Fatalf("解析手牌 %q: %v", text, err)
	}
	return hand
}

func mustTile(t *testing.T, text string) tile.Tile {
	t.Helper()
	tl, err := tile.ParseTile(text)
	if err != nil {
		t.Fatalf("解析牌 %q: %v", text, err)
	}
	return tl
}

// menzenWinContext 构造一个门清和了上下文。
func menzenWinContext(t *testing.T, handText, winning string, seat game.Seat, tsumo bool) game.WinContext {
	t.Helper()
	return game.WinContext{
		Seat:           seat,
		DealerSeat:     0,
		RonTarget:      1,
		Tsumo:          tsumo,
		Dealer:         seat == 0,
		RoundWind:      game.East,
		SeatWind:       game.Wind(seat),
		Hand:           mustHand(t, handText),
		WinningTile:    mustTile(t, winning),
		DoraIndicators: nil,
	}
}

func sumDeltas(deltas [4]int) int {
	total := 0
	for _, delta := range deltas {
		total += delta
	}
	return total
}

func TestScoreWinMenzenRon(t *testing.T) {
	// 123m456m789m123p55s 荣和 5s：门清，至少有一役（平和/一杯口等）
	ctx := menzenWinContext(t, "123m456m789m123p55s", "5s", 2, false)
	ctx.Riichi = true
	result, err := RulesScorer{}.ScoreWin(ctx)
	if err != nil {
		t.Fatalf("ScoreWin: %v", err)
	}
	if result.Han < 1 {
		t.Fatalf("门清和了至少 1 番，实际 %d（yaku=%v）", result.Han, result.Yaku)
	}
	if len(result.Yaku) == 0 {
		t.Fatalf("役种列表不应为空")
	}
	if sumDeltas(result.Deltas) != 0 {
		t.Fatalf("点数变化总和应为 0，实际 %v", result.Deltas)
	}
	if result.Deltas[1] >= 0 {
		t.Fatalf("放铳者应变负: %v", result.Deltas)
	}
	if result.Deltas[2] != -result.Deltas[1] {
		t.Fatalf("荣和应为一对一转移: %v", result.Deltas)
	}
}

func TestScoreWinMenzenTsumo(t *testing.T) {
	ctx := menzenWinContext(t, "123m456m789m123p55s", "5s", 2, true)
	ctx.Riichi = true
	ctx.Ippatsu = true
	result, err := RulesScorer{}.ScoreWin(ctx)
	if err != nil {
		t.Fatalf("ScoreWin: %v", err)
	}
	if sumDeltas(result.Deltas) != 0 {
		t.Fatalf("自摸点数变化总和应为 0，实际 %v", result.Deltas)
	}
	// 子家自摸：亲家付 2 倍、另外两家付 1 倍；和了者收入 = 三家支付之和
	paid := result.Deltas[0] + result.Deltas[1] + result.Deltas[3]
	if result.Deltas[2] != -paid {
		t.Fatalf("自摸收入应为三家支付之和: %v", result.Deltas)
	}
	if result.Deltas[0] >= result.Deltas[1] {
		t.Fatalf("亲家应比子家付得多（%v）", result.Deltas)
	}
}

func TestScoreWinHonbaIncreasesPoints(t *testing.T) {
	base := menzenWinContext(t, "123m456m789m123p55s", "5s", 2, false)
	withHonba := base
	withHonba.Honba = 2
	baseResult, err := RulesScorer{}.ScoreWin(base)
	if err != nil {
		t.Fatalf("ScoreWin base: %v", err)
	}
	honbaResult, err := RulesScorer{}.ScoreWin(withHonba)
	if err != nil {
		t.Fatalf("ScoreWin honba: %v", err)
	}
	if -honbaResult.Deltas[1] != -baseResult.Deltas[1]+600 {
		t.Fatalf("2 本场应让放铳者多付 600 点: base=%v honba=%v",
			baseResult.Deltas, honbaResult.Deltas)
	}
}

func TestScoreWinOpenHandUsesRealRules(t *testing.T) {
	// 碰 222p（明）+ 暗牌 234m567m345p55s：断幺九在副露下仍成立。
	ctx := menzenWinContext(t, "234m567m345p55s", "5s", 2, false)
	ctx.Melds = []game.Meld{{
		Kind:  game.MeldPon,
		Tiles: mustHand(t, "222p"),
		From:  1,
	}}
	result, err := RulesScorer{}.ScoreWin(ctx)
	if err != nil {
		t.Fatalf("ScoreWin: %v", err)
	}
	if result.Han < 1 {
		t.Fatalf("副露和了也应至少 1 番，实际 %d（yaku=%v）", result.Han, result.Yaku)
	}
	if strings.Contains(strings.Join(result.Yaku, ","), "近似") {
		t.Fatalf("S5b 起副露应走真实计分，不应回退近似: %v", result.Yaku)
	}
	if sumDeltas(result.Deltas) != 0 {
		t.Fatalf("点数变化总和应为 0: %v", result.Deltas)
	}
}

func TestScoreWinWithAnkanStaysMenzen(t *testing.T) {
	// 暗杠 1111m 不破门清：自摸应能拿到「门前清自摸和」。
	ctx := menzenWinContext(t, "234m567m234p55s", "5s", 2, true)
	ctx.Melds = []game.Meld{{
		Kind:  game.MeldKanAnkan,
		Tiles: mustHand(t, "1111m"),
		From:  2,
	}}
	result, err := RulesScorer{}.ScoreWin(ctx)
	if err != nil {
		t.Fatalf("ScoreWin: %v", err)
	}
	if result.Han < 1 {
		t.Fatalf("门清自摸 + 暗杠应至少 1 番，实际 %d（yaku=%v）", result.Han, result.Yaku)
	}
	if strings.Contains(strings.Join(result.Yaku, ","), "近似") {
		t.Fatalf("暗杠不破门清，应走真实计分: %v", result.Yaku)
	}
	if sumDeltas(result.Deltas) != 0 {
		t.Fatalf("点数变化总和应为 0: %v", result.Deltas)
	}
}

func TestScoreWinKokushiIsYakuman(t *testing.T) {
	ctx := menzenWinContext(t, "19m19p19s1234567z1m", "1m", 3, true)
	result, err := RulesScorer{}.ScoreWin(ctx)
	if err != nil {
		t.Fatalf("ScoreWin 国士: %v", err)
	}
	if result.Han < 13 {
		t.Fatalf("国士无双应为役满（≥13 番），实际 %d 番（yaku=%v）", result.Han, result.Yaku)
	}
	if sumDeltas(result.Deltas) != 0 {
		t.Fatalf("点数变化总和应为 0: %v", result.Deltas)
	}
}

func TestDeltasForRonMissingTargetIsZero(t *testing.T) {
	// RonTarget 越界时返回零值，避免把点数加到错误的座位上
	ctx := menzenWinContext(t, "123m456m789m123p55s", "5s", 2, false)
	ctx.RonTarget = 9
	result, err := RulesScorer{}.ScoreWin(ctx)
	if err == nil && result.Deltas != [4]int{} {
		t.Fatalf("非法 RonTarget 应返回零点数变化: %v", result.Deltas)
	}
}

func TestWindKind(t *testing.T) {
	cases := map[game.Wind]tile.Kind{
		game.East:  tile.East,
		game.South: tile.South,
		game.West:  tile.West,
		game.North: tile.North,
	}
	for wind, want := range cases {
		if got := windKind(wind); got != want {
			t.Fatalf("windKind(%v) = %v want %v", wind, got, want)
		}
	}
	// 越界值兜底为东
	if got := windKind(game.Wind(99)); got != tile.East {
		t.Fatalf("越界风位应兜底为东，实际 %v", got)
	}
}
