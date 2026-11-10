package rules

import (
	"errors"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// openCtx 返回副露手牌的场况：门清=false、子家（南）、场风东。
func openCtx() ScoreContext {
	return ctxWith(func(c *ScoreContext) { c.Menzen = false })
}

// evalMeldHand 解析「暗牌 + 副露」并调用 Decompose → Evaluate。
func evalMeldHand(t *testing.T, handText string, melds []Meld, winningText string, ctx ScoreContext) (WinShape, []Yaku, int, int) {
	t.Helper()
	hand := mustParseHand(t, handText)
	winning := tileFromName(t, winningText)
	shape, ok := Decompose(hand, melds, winning)
	if !ok {
		t.Fatalf("副露手牌应判和：hand=%q melds=%+v winning=%v", handText, melds, winning)
	}
	if ctx.Menzen != shapeMenzen(shape) {
		t.Fatalf("测试用例的 ctx.Menzen=%v 与 shape 明暗不一致", ctx.Menzen)
	}
	yaku, han, fu, err := Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate 返回错误：%v", err)
	}
	return shape, yaku, han, fu
}

// calledMeld 构造一条副露入参。
func calledMeld(t *testing.T, kind string, tileNames ...string) Meld {
	t.Helper()
	return Meld{Kind: kind, Tiles: parseTileNames(t, tileNames)}
}

// TestMeldFuMinkouVsAnkou 对照「明刻 2 符 vs 暗刻 4 符」：
// 同样的牌形，只把一组刻子从暗刻改成碰（副露），符数应少 4 符（这里 40 → 30）。
func TestMeldFuMinkouVsAnkou(t *testing.T) {
	// 暗刻版：暗牌里自己摸成的 555z（白）刻子。
	handAnkou := "456m 789m 555z 11z"
	meldsAnkou := []Meld{calledMeld(t, MeldChi, "1p", "2p", "3p")}
	shapeA, _, _, fuA := evalMeldHand(t, handAnkou, meldsAnkou, "1z", openCtx())
	verifyShapeWithMelds(t, mustParseHand(t, handAnkou), meldsAnkou, shapeA, tileFromName(t, "1z"))

	// 明刻版：同一组 555z 是碰出来的。
	handMinkou := "456m 789m 11z"
	meldsMinkou := []Meld{
		calledMeld(t, MeldChi, "1p", "2p", "3p"),
		calledMeld(t, MeldPon, "5z", "5z", "5z"),
	}
	// 暗牌 8 张 = 2 组面子 + 将牌。
	shapeB, _, _, fuB := evalMeldHand(t, handMinkou, meldsMinkou, "1z", openCtx())
	verifyShapeWithMelds(t, mustParseHand(t, handMinkou), meldsMinkou, shapeB, tileFromName(t, "1z"))

	// 计算过程：20 底 + 场风雀头 2 + 单骑 2 + 暗刻白 8 = 32 → 40；
	// 明刻版把 8 换成 4：20+2+2+4 = 28 → 30。
	if fuA != 40 {
		t.Fatalf("暗刻版符数 = %d，期望 40", fuA)
	}
	if fuB != 30 {
		t.Fatalf("明刻版符数 = %d，期望 30", fuB)
	}
	if fuA-fuB != 10 {
		t.Fatalf("明刻/暗刻符差 = %d，期望 10（切上后）", fuA-fuB)
	}
}

// TestMeldFuKanOpenVsClosed 对照「明杠 8 符 vs 暗杠 16 符」（幺九/字牌翻倍后 16 vs 32）。
func TestMeldFuKanOpenVsClosed(t *testing.T) {
	hand := "456m 789m 11z"

	openKan := []Meld{
		calledMeld(t, MeldChi, "1p", "2p", "3p"),
		calledMeld(t, MeldKanDaiminkan, "5z", "5z", "5z", "5z"),
	}
	shapeOpen, _, _, fuOpen := evalMeldHand(t, hand, openKan, "1z", openCtx())
	verifyShapeWithMelds(t, mustParseHand(t, hand), openKan, shapeOpen, tileFromName(t, "1z"))

	closedKan := []Meld{
		calledMeld(t, MeldChi, "1p", "2p", "3p"),
		calledMeld(t, MeldKanAnkan, "5z", "5z", "5z", "5z"),
	}
	shapeClosed, _, _, fuClosed := evalMeldHand(t, hand, closedKan, "1z", openCtx())
	verifyShapeWithMelds(t, mustParseHand(t, hand), closedKan, shapeClosed, tileFromName(t, "1z"))

	if fuOpen != 40 {
		t.Fatalf("明杠版符数 = %d，期望 40（20+2+2+16）", fuOpen)
	}
	if fuClosed != 60 {
		t.Fatalf("暗杠版符数 = %d，期望 60（20+2+2+32）", fuClosed)
	}
}

// TestMeldSanankou 三暗刻只数暗刻：3 组暗刻成立，2 组不成立。
func TestMeldSanankou(t *testing.T) {
	// 正例：暗牌里 3 组暗刻 + 副露 1 组吃。
	hand := "111m 222p 333s 99m"
	melds := []Meld{calledMeld(t, MeldChi, "1p", "2p", "3p")}
	_, yaku, _, _ := evalMeldHand(t, hand, melds, "9m", openCtx())
	requireYaku(t, yaku, YakuSanankou, 2)

	// 反例：2 组暗刻 + 1 组明刻 + 1 组吃。
	hand2 := "111m 222p 99m"
	melds2 := []Meld{
		calledMeld(t, MeldChi, "1p", "2p", "3p"),
		calledMeld(t, MeldPon, "9s", "9s", "9s"),
	}
	_, yaku2, _, _ := evalMeldHand(t, hand2, melds2, "9m", openCtx())
	requireNoYaku(t, yaku2, YakuSanankou)
}

// TestMeldYakuKuikudari 覆盖副露降翻（喰い下がり）：
// 混一色 3→2、清一色 6→5、三色同顺 2→1、一气通贯 2→1。
func TestMeldYakuKuikudari(t *testing.T) {
	cases := []struct {
		name    string
		hand    string
		melds   []Meld
		winning string
		yaku    string
		wantHan int
	}{
		{
			"混一色副露 2 翻",
			"456m 789m 555z 22z",
			[]Meld{calledMeld(t, MeldChi, "1m", "2m", "3m")},
			"2z", YakuHonitsu, 2,
		},
		{
			"清一色副露 5 翻",
			"456m 789m 234m 99m",
			[]Meld{calledMeld(t, MeldChi, "1m", "2m", "3m")},
			"9m", YakuChinitsu, 5,
		},
		{
			"三色同顺副露 1 翻",
			"123m 123s 456m 99s",
			[]Meld{calledMeld(t, MeldChi, "1p", "2p", "3p")},
			"3s", YakuSanshokuDoujun, 1,
		},
		{
			"一气通贯副露 1 翻",
			"456m 789m 555z 99p",
			[]Meld{calledMeld(t, MeldChi, "1m", "2m", "3m")},
			"9p", YakuIttsu, 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape, yaku, _, _ := evalMeldHand(t, tc.hand, tc.melds, tc.winning, openCtx())
			verifyShapeWithMelds(t, mustParseHand(t, tc.hand), tc.melds, shape, tileFromName(t, tc.winning))
			requireYaku(t, yaku, tc.yaku, tc.wantHan)
		})
	}
}

// TestMeldWaitTypes 副露后待ち类型仍要正确：嵌张/两面/边张/双碰/单骑。
func TestMeldWaitTypes(t *testing.T) {
	cases := []struct {
		name    string
		hand    string
		melds   []Meld
		winning string
		want    WaitType
	}{
		{
			"嵌张",
			"123m 456m 456p 99s",
			[]Meld{calledMeld(t, MeldPon, "5z", "5z", "5z")},
			"5p", WaitKanchan,
		},
		{
			"两面",
			"123m 456m 456s 99p",
			[]Meld{calledMeld(t, MeldChi, "1p", "2p", "3p")},
			"6s", WaitRyanmen,
		},
		{
			"边张",
			"123m 456m 123s 99p",
			[]Meld{calledMeld(t, MeldChi, "7p", "8p", "9p")},
			"3s", WaitPenchan,
		},
		{
			"双碰",
			"111m 234p 567p 99s",
			[]Meld{calledMeld(t, MeldChi, "1p", "2p", "3p")},
			"1m", WaitShanpon,
		},
		{
			"单骑",
			"123m 456m 789m 11p",
			[]Meld{calledMeld(t, MeldPon, "5z", "5z", "5z")},
			"1p", WaitTanki,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			winning := tileFromName(t, tc.winning)
			shape, ok := Decompose(hand, tc.melds, winning)
			if !ok {
				t.Fatalf("应判和：hand=%q melds=%+v", tc.hand, tc.melds)
			}
			if shape.Wait != tc.want {
				t.Fatalf("Wait = %q，期望 %q", shape.Wait, tc.want)
			}
			verifyShapeWithMelds(t, hand, tc.melds, shape, winning)
		})
	}
}

// TestMeldMenzenBonus 门清 +10 符只在没有副露时生效。
//
// 用「役牌雀头」的非平和形来对照，避免撞上「喰い平和形荣和固定 30 符」的特例：
// 门清：20 底 + 10 门清荣和 + 2 场风雀头 = 32 → 40；
// 副露：20 底 + 2 场风雀头 = 22 → 30。
func TestMeldMenzenBonus(t *testing.T) {
	// 无副露：4 组顺子 + 场风雀头 11z，门清荣和。
	closedHand := "123m 456m 789m 123p 11z"
	closedShape, ok := Decompose(mustParseHand(t, closedHand), nil, tileFromName(t, "6m"))
	if !ok {
		t.Fatal("门清手牌应判和")
	}
	closedFu, err := Fu(closedShape, plainCtx())
	if err != nil {
		t.Fatalf("Fu 返回错误：%v", err)
	}
	if closedFu != 40 {
		t.Fatalf("门清荣和符 = %d，期望 40", closedFu)
	}

	// 有副露（吃 123m）：其余顺子相同，但门清 +10 不生效。
	openHand := "456m 789m 123p 11z"
	openMelds := []Meld{calledMeld(t, MeldChi, "1m", "2m", "3m")}
	openShape, ok := Decompose(mustParseHand(t, openHand), openMelds, tileFromName(t, "6m"))
	if !ok {
		t.Fatal("副露手牌应判和")
	}
	openFu, err := Fu(openShape, openCtx())
	if err != nil {
		t.Fatalf("Fu 返回错误：%v", err)
	}
	if openFu != 30 {
		t.Fatalf("副露荣和符 = %d，期望 30（无门清加成）", openFu)
	}
	if closedFu-openFu != 10 {
		t.Fatalf("门清加成的差值 = %d，期望 10", closedFu-openFu)
	}
}

// TestMeldMenzenConsistency 断言 ctx.Menzen 与副露 Open 不一致时 Evaluate/Fu 报错；
// 只有暗杠的副露仍算门清。
func TestMeldMenzenConsistency(t *testing.T) {
	hand := "456m 789m 555z 11z"
	openMelds := []Meld{calledMeld(t, MeldChi, "1p", "2p", "3p")}
	shape, ok := Decompose(mustParseHand(t, hand), openMelds, tileFromName(t, "1z"))
	if !ok {
		t.Fatal("应判和")
	}
	if _, _, _, err := Evaluate(shape, plainCtx()); !errors.Is(err, ErrInvalidContext) {
		t.Fatalf("ctx.Menzen=true 但副露是明面，应返回 ErrInvalidContext，实际 %v", err)
	}

	// 只有暗杠：Open=false，门清仍成立，ctx.Menzen=true 合法。
	ankanHand := "234m 345p 678s 99p"
	ankanMelds := []Meld{calledMeld(t, MeldKanAnkan, "1m", "1m", "1m", "1m")}
	ankanShape, ok := Decompose(mustParseHand(t, ankanHand), ankanMelds, tileFromName(t, "9p"))
	if !ok {
		t.Fatal("暗杠手牌应判和")
	}
	if !shapeMenzen(ankanShape) {
		t.Fatal("只有暗杠的副露仍应算门清")
	}
	if _, _, _, err := Evaluate(ankanShape, plainCtx()); err != nil {
		t.Fatalf("暗杠 + 门清 ctx 应当合法，实际错误：%v", err)
	}
}

// TestMeldDoraCounts 宝牌统计要包含副露里的牌。
func TestMeldDoraCounts(t *testing.T) {
	hand := "456m 789m 11z"
	melds := []Meld{
		calledMeld(t, MeldChi, "1p", "2p", "3p"),
		calledMeld(t, MeldPon, "5z", "5z", "5z"),
	}
	shape, ok := Decompose(mustParseHand(t, hand), melds, tileFromName(t, "1z"))
	if !ok {
		t.Fatal("应判和")
	}
	// 指示牌 4z（北）→ 宝牌 1z（东）：暗牌有 2 张 1z，应计 2 张。
	ctx := openCtx()
	ctx.DoraIndicators = []tile.Tile{tileFromName(t, "4z")}
	if got := CountDora(shape, ctx); got != 2 {
		t.Fatalf("宝牌数 = %d，期望 2", got)
	}
	// 指示牌 4z 同时能指向副露里的牌时也要统计：指示牌 4z→1z 不含副露；
	// 换一个指向副露牌的例子：指示牌 2p → 宝牌 3p（副露吃 123p 里有 1 张 3p）。
	ctx2 := openCtx()
	ctx2.DoraIndicators = []tile.Tile{tileFromName(t, "2p")}
	if got := CountDora(shape, ctx2); got != 1 {
		t.Fatalf("指向副露牌的宝牌数 = %d，期望 1", got)
	}
}
