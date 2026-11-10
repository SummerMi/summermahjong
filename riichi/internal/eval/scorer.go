package eval

import (
	"fmt"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// RulesScorer 用 rules 包的真实役/符/点计分（game.Options.ScoreProvider 的实现）。
//
// 覆盖范围：
//   - 门清与副露（吃/碰/明杠/加杠/暗杠）和了：把 game.Meld 转成 rules.Meld 后调用
//     rules.Decompose(暗牌, 副露, 和了牌) → Evaluate（役种/翻/符）→ 宝牌/赤宝牌/里宝牌
//     加成 → rules.Score 得到点数，再拆成四家净点数变化（引擎要求总和为 0）。
//     S5b（commit f37b275）起 rules.Decompose 已支持副露，因此不再需要近似。
//   - 极端兜底：引擎判定和了但 rules 拆不出/无役时，回退「1 翻 30 符」保证对局继续，
//     并在役种列表里标注「近似计分」，便于从事件里识别。
//
// RulesScorer 无状态，可被并发复用。
type RulesScorer struct{}

// ScoreWin 实现 game.ScoreProvider。
func (RulesScorer) ScoreWin(ctx game.WinContext) (game.WinResult, error) {
	rulesMelds, menzen := convertMelds(ctx.Melds)
	scoreCtx := rules.ScoreContext{
		Menzen:         menzen,
		Tsumo:          ctx.Tsumo,
		Riichi:         ctx.Riichi,
		Ippatsu:        ctx.Ippatsu,
		Rinshan:        ctx.Rinshan,
		Chankan:        ctx.Chankan,
		Haitei:         ctx.Haitei,
		Houtei:         ctx.Houtei,
		Tenhou:         ctx.Tenhou,
		Chiihou:        ctx.Chiihou,
		SeatWind:       windKind(ctx.SeatWind),
		RoundWind:      windKind(ctx.RoundWind),
		Honba:          ctx.Honba,
		RiichiSticks:   0, // 立直棒由引擎在结算时加给和了者，Provider 不重复计算
		DoraIndicators: ctx.DoraIndicators,
	}

	if result, ok := scoreWithRules(ctx, rulesMelds, scoreCtx); ok {
		return result, nil
	}
	// 兜底：1 翻 30 符近似，保证流程可继续、点数自洽。
	points := rules.Score(1, 30, scoreCtx)
	return game.WinResult{
		Han:    1,
		Fu:     30,
		Yaku:   []string{"和了（近似计分）"},
		Deltas: deltasFor(points, ctx),
	}, nil
}

// convertMelds 把 game.Meld 转成 rules.Meld，并判断是否门清（只有暗杠保持门清）。
func convertMelds(melds []game.Meld) ([]rules.Meld, bool) {
	if len(melds) == 0 {
		return nil, true
	}
	out := make([]rules.Meld, 0, len(melds))
	menzen := true
	for _, meld := range melds {
		if meld.Kind != game.MeldKanAnkan {
			menzen = false
		}
		out = append(out, rules.Meld{Kind: string(meld.Kind), Tiles: meld.Tiles})
	}
	return out, menzen
}

// scoreWithRules 用 rules 计算一次和了；无法拆解/无役时返回 ok=false 走近似。
func scoreWithRules(
	ctx game.WinContext,
	rulesMelds []rules.Meld,
	scoreCtx rules.ScoreContext,
) (game.WinResult, bool) {
	shape, ok := rules.Decompose(ctx.Hand, rulesMelds, ctx.WinningTile)
	if !ok {
		return game.WinResult{}, false
	}
	scoreCtx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, scoreCtx)
	// 里宝牌：只有立直和了才翻开，单独统计。
	if ctx.Riichi && len(ctx.UraIndicators) > 0 {
		uraCtx := scoreCtx
		uraCtx.DoraIndicators = ctx.UraIndicators
		uraCtx.AkaDora = 0
		dora += rules.CountDoraFromTiles(rules.ShapeTiles(shape), uraCtx)
	}
	scoreCtx.DoraCount = dora

	yaku, han, fu, err := rules.Evaluate(shape, scoreCtx)
	if err != nil {
		return game.WinResult{}, false
	}
	totalHan := han + dora
	if totalHan <= 0 {
		return game.WinResult{}, false // 无役：按近似结算，避免 0 点数和了
	}
	points := rules.Score(totalHan, fu, scoreCtx)
	names := make([]string, 0, len(yaku)+1)
	for _, item := range yaku {
		names = append(names, item.Name)
	}
	if dora > 0 {
		names = append(names, fmt.Sprintf("宝牌×%d", dora))
	}
	return game.WinResult{
		Han:    totalHan,
		Fu:     fu,
		Yaku:   names,
		Deltas: deltasFor(points, ctx),
	}, true
}

// deltasFor 把 rules.Points 拆成四家净点数变化（含本场；立直棒由引擎另加）。
func deltasFor(points rules.Points, ctx game.WinContext) [4]int {
	var deltas [4]int
	winner := int(ctx.Seat)
	if ctx.Tsumo {
		for seat := 0; seat < 4; seat++ {
			if seat == winner {
				continue
			}
			pay := points.TsumoChild
			if game.Seat(seat) == ctx.DealerSeat {
				pay = points.TsumoDealer
			}
			// 每本场自摸三家各 +100（与 rules.Score 的 bonus 口径一致）。
			pay += 100 * max(ctx.Honba, 0)
			deltas[seat] -= pay
			deltas[winner] += pay
		}
		return deltas
	}
	target := int(ctx.RonTarget)
	if target < 0 || target >= 4 {
		return deltas
	}
	deltas[target] -= points.Ron
	deltas[winner] += points.Ron
	return deltas
}

// windKind 把 game.Wind 映射成 tile.Kind（东南西北字牌）。
func windKind(w game.Wind) tile.Kind {
	switch w {
	case game.East:
		return tile.East
	case game.South:
		return tile.South
	case game.West:
		return tile.West
	case game.North:
		return tile.North
	default:
		return tile.East
	}
}
