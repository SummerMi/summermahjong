package rules

import (
	"strings"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// plainCtx 返回一个最常见的场况：门清、荣和、子家（南）、场风东、无立直/宝牌。
func plainCtx() ScoreContext {
	return ScoreContext{Menzen: true, SeatWind: tile.South, RoundWind: tile.East}
}

// ctxWith 以 plainCtx 为底，用回调改字段，方便表驱动测试书写。
func ctxWith(mod func(*ScoreContext)) ScoreContext {
	ctx := plainCtx()
	mod(&ctx)
	return ctx
}

// evalHand 解析 14 张手牌与和了牌，Decompose 后调用 Evaluate。
func evalHand(t *testing.T, handText, winningText string, ctx ScoreContext) (WinShape, []Yaku, int, int) {
	t.Helper()
	hand := mustParseHand(t, handText)
	winning := tileFromName(t, winningText)
	shape, ok := Decompose(hand, nil, winning)
	if !ok {
		t.Fatalf("Decompose(%q, winning=%v) 应判和牌", handText, winning)
	}
	if !ctx.Menzen {
		t.Fatalf("Decompose 产出的是门清形状，ctx.Menzen 必须为 true")
	}
	yaku, han, fu, err := Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate(%q) 返回错误：%v", handText, err)
	}
	return shape, yaku, han, fu
}

// parseMeldSpec 解析手工构造面子用的规格字符串，例如：
//
//	"chi:123m"          → 吃的顺子（Open=true）
//	"pon:5z"            → 碰（Open=true）
//	"pon_closed:5z"     → 暗刻（Open=false，测试用）
//	"kan_daiminkan:9p"  → 大明杠（4 张，Open=true）
//	"kan_ankan:1m"      → 暗杠（4 张，Open=false）
func parseMeldSpec(t *testing.T, spec string) MeldShape {
	t.Helper()
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("面子规格 %q 缺少冒号", spec)
	}
	kind, text := parts[0], parts[1]
	tiles := mustParseHand(t, text)

	switch kind {
	case MeldChi, "chi_closed":
		if len(tiles) != 3 || !isChiTiles(tiles) {
			t.Fatalf("吃的规格 %q 不是合法顺子", spec)
		}
		return MeldShape{Kind: MeldChi, Tiles: tiles, Open: kind == MeldChi}
	case MeldPon, "pon_closed":
		if len(tiles) != 1 {
			t.Fatalf("碰的规格 %q 应只写一张牌", spec)
		}
		out := []tile.Tile{tiles[0], tiles[0], tiles[0]}
		return MeldShape{Kind: MeldPon, Tiles: out, Open: kind == MeldPon}
	case MeldKanDaiminkan, MeldKanShouminkan, MeldKanAnkan:
		if len(tiles) != 1 {
			t.Fatalf("杠的规格 %q 应只写一张牌", spec)
		}
		out := []tile.Tile{tiles[0], tiles[0], tiles[0], tiles[0]}
		return MeldShape{Kind: kind, Tiles: out, Open: kind != MeldKanAnkan}
	default:
		t.Fatalf("未知面子规格类型 %q", kind)
		return MeldShape{}
	}
}

// buildShape 手工构造 WinShape（用于 Decompose 还不支持的副露/杠形状）。
func buildShape(t *testing.T, pattern, jantouText string, wait WaitType, specs ...string) WinShape {
	t.Helper()
	jantou := tileFromName(t, jantouText)
	shape := WinShape{
		Jantou:  jantou,
		Wait:    wait,
		Pattern: pattern,
		Melds:   make([]MeldShape, 0, len(specs)),
	}
	shape.Tiles = append(shape.Tiles, jantou, jantou)
	for _, spec := range specs {
		meld := parseMeldSpec(t, spec)
		shape.Melds = append(shape.Melds, meld)
		shape.Tiles = append(shape.Tiles, meld.Tiles...)
	}
	return shape
}

// evalShape 对手工构造的 shape 调用 Evaluate。
func evalShape(t *testing.T, shape WinShape, ctx ScoreContext) ([]Yaku, int, int) {
	t.Helper()
	if ctx.Menzen != shapeMenzen(shape) {
		t.Fatalf("测试用例的 ctx.Menzen=%v 与 shape 明暗不一致", ctx.Menzen)
	}
	yaku, han, fu, err := Evaluate(shape, ctx)
	if err != nil {
		t.Fatalf("Evaluate 返回错误：%v", err)
	}
	return yaku, han, fu
}

// yakuNames 返回役种名列表（便于失败信息阅读）。
func yakuNames(yaku []Yaku) []string {
	names := make([]string, 0, len(yaku))
	for _, y := range yaku {
		names = append(names, y.Name)
	}
	return names
}

// findYaku 返回指定役的存在性与翻数。
func findYaku(yaku []Yaku, name string) (int, bool) {
	for _, y := range yaku {
		if y.Name == name {
			return y.Han, true
		}
	}
	return 0, false
}

// requireYaku 断言某役成立且翻数等于 wantHan。
func requireYaku(t *testing.T, yaku []Yaku, name string, wantHan int) {
	t.Helper()
	han, ok := findYaku(yaku, name)
	if !ok {
		t.Fatalf("缺少役种 %q，实际役种：%v", name, yakuNames(yaku))
	}
	if han != wantHan {
		t.Fatalf("役种 %q 翻数 = %d，期望 %d（实际役种：%v）", name, han, wantHan, yakuNames(yaku))
	}
}

// requireNoYaku 断言某役不成立。
func requireNoYaku(t *testing.T, yaku []Yaku, name string) {
	t.Helper()
	if _, ok := findYaku(yaku, name); ok {
		t.Fatalf("不应出现役种 %q，实际役种：%v", name, yakuNames(yaku))
	}
}

// verifyShapeWithMelds 校验带副露的拆解结果：
//   - 4 组面子 + 将牌，用牌总数 = 暗牌 + 副露（杠为 4 张）；
//   - 每一条入参副露都能在 shape.Melds 里找到，且 Open 正确（暗杠 false，其余 true）；
//   - 暗牌拆出来的面子 Open 全为 false。
func verifyShapeWithMelds(t *testing.T, hand []tile.Tile, melds []Meld, shape WinShape, winning tile.Tile) {
	t.Helper()
	if shape.Wait == "" {
		t.Fatal("待ち类型为空")
	}
	if kindCounts(flattenShape(shape))[winning.Kind] == 0 {
		t.Fatalf("拆解结果里找不到和了牌 %v", winning)
	}

	// 全部物理牌 = 暗牌 + 副露牌。
	var wantTiles [tile.NumKinds]int
	for _, x := range hand {
		wantTiles[x.Kind]++
	}
	for _, meld := range melds {
		for _, x := range meld.Tiles {
			wantTiles[x.Kind]++
		}
	}
	if got := kindCounts(shape.Tiles); got != wantTiles {
		t.Fatalf("shape.Tiles 与暗牌+副露不一致：%v vs %v", got, wantTiles)
	}

	// 结构：4 组面子 + 将牌，正好用完所有牌。
	var gotTiles [tile.NumKinds]int
	gotTiles[shape.Jantou.Kind] += 2
	for _, meld := range shape.Melds {
		if !validMeldShape(meld) {
			t.Fatalf("拆解出的面子非法：%+v", meld)
		}
		for _, x := range meld.Tiles {
			gotTiles[x.Kind]++
		}
	}
	if gotTiles != wantTiles {
		t.Fatalf("面子+将牌的用牌与暗牌+副露不一致：%v vs %v", gotTiles, wantTiles)
	}

	// 每条入参副露都要原样保留（Kind/Tiles/Open）。
	for _, called := range melds {
		wantOpen := called.Kind != MeldKanAnkan
		found := false
		for _, meld := range shape.Melds {
			if meld.Kind != called.Kind || len(meld.Tiles) != len(called.Tiles) {
				continue
			}
			same := true
			for i := range called.Tiles {
				if meld.Tiles[i].Kind != called.Tiles[i].Kind {
					same = false
					break
				}
			}
			if !same {
				continue
			}
			if meld.Open != wantOpen {
				t.Fatalf("副露 %q 的 Open = %v，期望 %v", called.Kind, meld.Open, wantOpen)
			}
			found = true
			break
		}
		if !found {
			t.Fatalf("拆解结果里没有保留副露：%+v（shape=%+v）", called, shape.Melds)
		}
	}

	// 其余（暗牌拆出的）面子必须是暗面。
	openExpected := 0
	for _, called := range melds {
		if called.Kind != MeldKanAnkan {
			openExpected++
		}
	}
	openActual := 0
	for _, meld := range shape.Melds {
		if meld.Open {
			openActual++
		}
	}
	if openActual != openExpected {
		t.Fatalf("Open 面子数 = %d，期望 %d（仅副露明面）", openActual, openExpected)
	}
}
