package rules

import "summermahjong/riichi/internal/tile"

// Points 是一次和了的点数结算结果。
//
// 亲/子与自摸/荣和的分配约定：
//   - 荣和：Ron = 放铳者支付给和了者的点数（已含本场与立直棒），Total = Ron；
//   - 自摸：TsumoChild = 每个子家支付、TsumoDealer = 亲家支付，
//     Total = 各家支付之和 + 本场 300/本 + 立直棒 1000/根；
//   - 亲家自摸时所有家都付 TsumoDealer，此时 TsumoChild 与 TsumoDealer 相同。
type Points struct {
	Han         int // 含宝牌的最终翻数
	Fu          int // 符（切上后）
	Base        int // 基本点
	Ron         int // 荣和支付（含本场/立直棒）；自摸时为 0
	TsumoChild  int // 自摸时每个子家支付（不含本场）
	TsumoDealer int // 自摸时亲家支付（不含本场）
	Total       int // 和了者总共得到（含本场与立直棒）
}

// Fu 计算和牌符数（契约 §3 的 S4 附加导出）。
//
// 规则（天凤般）：
//   - 副底 20 符；
//   - 门清荣和 +10 符（仅荣和，自摸不加）；
//   - 自摸 +2 符；
//   - 雀头：三元牌 / 自风 / 场风各 +2（连风合计 +4）；
//   - 待ち：嵌张 / 边张 / 单骑 +2，两面 +0；
//   - 明刻 2 / 暗刻 4，明杠 8 / 暗杠 16；幺九牌与字牌翻倍；
//   - 平和自摸固定 20 符；喰い平和形荣和（副露的平和形）固定 30 符；
//   - 七对子固定 25 符；国士无双按 25 符展示（役满不计符）；
//   - 最后切上到 10 符（20+2+2=24 → 30）。
//
// 返回 ErrInvalidShape / ErrInvalidContext 表示输入不自洽。
func Fu(shape WinShape, ctx ScoreContext) (int, error) {
	if err := validateShape(shape); err != nil {
		return 0, err
	}
	if err := validateContext(shape, ctx); err != nil {
		return 0, err
	}

	switch shape.Pattern {
	case PatternSevenPairs:
		return 25, nil
	case PatternKokushi:
		return 25, nil // 役满不计符；这里返回 25 仅用于展示
	}

	menzen := ctx.Menzen
	pinfu := pinfuShapeAny(shape, ctx)
	// 平和形：门清自摸固定 20 符；非门清（喰い平和形）荣和固定 30 符。
	if menzen && ctx.Tsumo && pinfu {
		return 20, nil
	}
	if !menzen && !ctx.Tsumo && pinfu {
		return 30, nil
	}

	fu := 20
	if menzen && !ctx.Tsumo {
		fu += 10
	}
	if ctx.Tsumo {
		fu += 2
	}
	fu += valuePairFu(shape.Jantou.Kind, ctx)
	switch shape.Wait {
	case WaitKanchan, WaitPenchan, WaitTanki:
		fu += 2
	}
	for _, meld := range shape.Melds {
		if meld.Kind == MeldChi {
			continue
		}
		fu += meldFu(meld)
	}
	return ceil10(fu), nil
}

// pinfuShapeAny 只检查平和的「形」：四组顺子 + 非役牌雀头 + 两面待ち，不看门清。
//
// 门清时它就是平和役的成立条件（配合 pinfuShape）；非门清时用于
// 「喰い平和形荣和 = 30 符」的特例。
func pinfuShapeAny(shape WinShape, ctx ScoreContext) bool {
	if shape.Pattern != PatternStandard {
		return false
	}
	for _, meld := range shape.Melds {
		if meld.Kind != MeldChi {
			return false
		}
	}
	return !isValuePair(shape.Jantou.Kind, ctx) && shape.Wait == WaitRyanmen
}

// meldFu 返回一组刻子/杠的符数：明刻 2 / 暗刻 4，明杠 8 / 暗杠 16；幺九字牌翻倍。
func meldFu(meld MeldShape) int {
	isKan := len(meld.Tiles) == 4
	var fu int
	switch {
	case isKan && meld.Open:
		fu = 8
	case isKan:
		fu = 16
	case meld.Open:
		fu = 2
	default:
		fu = 4
	}
	if meld.Tiles[0].Kind.IsTerminalOrHonor() {
		fu *= 2
	}
	return fu
}

// basePoints 由翻数与符数求基本点，并处理满贯以上的封顶。
//
// 役满按「13 翻 = 1 倍役满」编码（Evaluate 里每个役满役记 13 翻），
// 因此 han>=13 时基本点 = 8000 × (han/13)。
func basePoints(han, fu int) int {
	switch {
	case han >= 13:
		return 8000 * (han / 13)
	case han >= 11:
		return 6000 // 三倍满
	case han >= 8:
		return 4000 // 倍满
	case han >= 6:
		return 3000 // 跳满
	case han >= 5:
		return 2000 // 满贯
	case han == 4 && fu >= 40:
		return 2000 // 4 翻 40 符以上按满贯
	case han == 3 && fu >= 70:
		return 2000 // 3 翻 70 符以上按满贯
	}
	return fu * (1 << (han + 2))
}

// ceil100 向上取整到 100 点（点数支付用）。
func ceil100(n int) int {
	if n <= 0 {
		return 0
	}
	return (n + 99) / 100 * 100
}

// ceil10 向上取整到 10 符（符计算用）。
func ceil10(n int) int {
	if n <= 0 {
		return 0
	}
	return (n + 9) / 10 * 10
}

// Score 由「翻数 + 符数」计算点数（契约 §3）。
//
// 重要约定（与 S5 的衔接）：
//   - han 必须是**含宝牌的最终翻数**（Evaluate 返回的 han 不含宝牌）。
//     宝牌/赤宝牌/里宝牌的翻数由调用方统计后自己加上去（可参考 ctx.DoraCount / CountDora）；
//     Score 不会重复加宝牌。
//   - fu 使用 Fu 的返回值；役满时 fu 不参与计算。
//   - ctx.Honba 每本场 +300（自摸时三家各 +100）；ctx.RiichiSticks 每根 +1000，都归和了者。
//   - ctx.SeatWind == East 视为亲家，其余为子家。
//
// han <= 0 时返回零值（调用方应先判断是否和了）。
func Score(han, fu int, ctx ScoreContext) Points {
	pts := Points{Han: han, Fu: fu}
	if han <= 0 || han < 0 {
		return pts
	}
	if fu < 0 {
		fu = 0
	}
	base := basePoints(han, fu)
	pts.Base = base

	honba := ctx.Honba
	if honba < 0 {
		honba = 0
	}
	sticks := ctx.RiichiSticks
	if sticks < 0 {
		sticks = 0
	}
	bonus := honba*300 + sticks*1000

	if ctx.SeatWind == tile.East {
		// 亲家：荣和 6 倍基本点；自摸三家各付 2 倍基本点。
		ron := ceil100(6*base) + bonus
		pay := ceil100(2 * base)
		pts.Ron = ron
		pts.TsumoDealer = pay
		pts.TsumoChild = pay
		if ctx.Tsumo {
			pts.Ron = 0
			pts.Total = 3*pay + bonus
		} else {
			pts.TsumoDealer = 0
			pts.TsumoChild = 0
			pts.Total = ron
		}
		return pts
	}

	// 子家：荣和 4 倍基本点；自摸子家付 1 倍、亲家付 2 倍基本点。
	ron := ceil100(4*base) + bonus
	childPay := ceil100(base)
	dealerPay := ceil100(2 * base)
	pts.Ron = ron
	pts.TsumoChild = childPay
	pts.TsumoDealer = dealerPay
	if ctx.Tsumo {
		pts.Ron = 0
		pts.Total = 2*childPay + dealerPay + bonus
	} else {
		pts.TsumoChild = 0
		pts.TsumoDealer = 0
		pts.Total = ron
	}
	return pts
}

// CountAka 统计 shape 里的赤宝牌枚数（S5 可用它填 ctx.AkaDora）。
func CountAka(shape WinShape) int {
	n := 0
	for _, t := range ShapeTiles(shape) {
		if t.Aka {
			n++
		}
	}
	return n
}

// CountDora 统计表宝牌 + 赤宝牌的翻数（不含里宝牌，里宝牌由 S5 立直和了时另算）。
//
// 结果为「宝牌翻数」，调用方把它加到 Evaluate 返回的 han 上再传给 Score。
// 注意：ctx.DoraCount 只是元数据，Evaluate/Score 都不会自动加宝牌，避免重复计算。
func CountDora(shape WinShape, ctx ScoreContext) int {
	return CountDoraFromTiles(ShapeTiles(shape), ctx)
}

// CountDoraFromTiles 与 CountDora 相同，但直接统计原始牌列表。
//
// 副露/杠的牌若不在 shape 里，调用方可以把手牌 + 副露牌一起传进来。
func CountDoraFromTiles(tiles []tile.Tile, ctx ScoreContext) int {
	counts := make([]int, tile.NumKinds)
	for _, t := range tiles {
		if t.Kind < tile.NumKinds {
			counts[t.Kind]++
		}
	}
	total := ctx.AkaDora
	for _, indicator := range ctx.DoraIndicators {
		dora, ok := doraFromIndicator(indicator.Kind)
		if ok {
			total += counts[dora]
		}
	}
	return total
}

// doraFromIndicator 返回宝牌指示牌对应的宝牌：数牌 1→2…9→1，风牌与三元牌各自循环。
func doraFromIndicator(kind tile.Kind) (tile.Kind, bool) {
	if kind >= tile.NumKinds {
		return 0, false
	}
	switch {
	case kind.IsWind():
		return tile.East + (kind-tile.East+1)%4, true
	case kind.IsDragon():
		return tile.Haku + (kind-tile.Haku+1)%3, true
	case kind%9 == 8:
		return kind - 8, true
	default:
		return kind + 1, true
	}
}
