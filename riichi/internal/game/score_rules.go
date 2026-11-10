package game

import (
	"fmt"

	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// RulesScorer 是生产用计分实现：把和牌交给 package rules 拆解、计役/符/点。
//
// 与占位实现（1 翻 30 符）的关系：S5 先用占位把流程跑通，S5b（task-14）接入真实算点。
// 现在 game.New 默认使用本实现；Options.ScoreProvider 仍可注入其它实现（测试/替换）。
//
// 契约口径（v1.2）：
//   - rules.Evaluate 返回的 han 不含宝牌；这里在 Evaluate 之后把
//     CountDora（表宝牌 + 赤宝牌）与里宝牌翻数加到 han 上，再调用 rules.Score；
//   - rules.Score 的 Points 已含本场（Honba）；立直棒由引擎统一加给首个和了者，
//     所以给 rules.Score 传 RiichiSticks = 0，避免重复计分；
//   - 副露（吃/碰/明杠/加杠/暗杠）直接交给 rules.Decompose 的原生 melds 支持
//     （task-13，commit f37b275）：closedHand 为 14-3*len(melds) 张、杠为 4 张，
//     Open 语义与门清判定由 rules 负责，暗杠不破门清。
type RulesScorer struct{}

// ScoreWin 实现 ScoreProvider。
func (RulesScorer) ScoreWin(ctx WinContext) (WinResult, error) {
	shape, ok := decomposeWin(ctx.Hand, ctx.Melds, ctx.WinningTile)
	if !ok {
		return WinResult{}, fmt.Errorf("%w: 手牌无法拆解为和牌形", ErrIllegalAction)
	}

	scoreCtx := buildScoreContext(shape, ctx)
	yaku, han, fu, err := rules.Evaluate(shape, scoreCtx)
	if err != nil {
		return WinResult{}, fmt.Errorf("%w: 计分失败: %v", ErrIllegalAction, err)
	}

	// 宝牌：表宝牌 + 赤宝牌；立直和了时再加里宝牌。
	scoreCtx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, scoreCtx)
	if ctx.Riichi && len(ctx.UraIndicators) > 0 {
		dora += rules.CountDoraFromTiles(rules.ShapeTiles(shape), rules.ScoreContext{
			DoraIndicators: ctx.UraIndicators,
		})
	}
	scoreCtx.DoraCount = dora
	finalHan := han + dora

	points := rules.Score(finalHan, fu, scoreCtx)
	deltas, err := winDeltas(points, ctx)
	if err != nil {
		return WinResult{}, err
	}

	names := make([]string, 0, len(yaku))
	for _, y := range yaku {
		names = append(names, y.Name)
	}
	return WinResult{
		Han:    finalHan,
		Fu:     fu,
		Yaku:   names,
		Deltas: deltas,
	}, nil
}

// winDeltas 把 rules.Points 转成四家净点数变化。
//
// rules.Score 的 TsumoChild/TsumoDealer 不含本场（其注释约定），而 Total/Ron 含本场，
// 所以自摸时每家额外支付 100×本场；荣和直接用 Ron（已含本场 300/本）。
func winDeltas(points rules.Points, ctx WinContext) ([4]int, error) {
	var deltas [4]int
	if ctx.Tsumo {
		honbaEach := 100 * ctx.Honba
		for seat := 0; seat < 4; seat++ {
			if Seat(seat) == ctx.Seat {
				continue
			}
			pay := points.TsumoChild
			if Seat(seat) == ctx.DealerSeat {
				pay = points.TsumoDealer
			}
			pay += honbaEach
			deltas[seat] -= pay
			deltas[ctx.Seat] += pay
		}
		return deltas, nil
	}
	if ctx.RonTarget >= 4 {
		return deltas, fmt.Errorf("%w: 荣和缺少放铳者", ErrIllegalAction)
	}
	deltas[ctx.RonTarget] -= points.Ron
	deltas[ctx.Seat] += points.Ron
	return deltas, nil
}

// buildScoreContext 把 game 的 WinContext 映射成 rules.ScoreContext。
func buildScoreContext(shape rules.WinShape, ctx WinContext) rules.ScoreContext {
	return rules.ScoreContext{
		Menzen:         shapeMenzen(shape),
		Tsumo:          ctx.Tsumo,
		Riichi:         ctx.Riichi,
		Ippatsu:        ctx.Ippatsu,
		Rinshan:        ctx.Rinshan,
		Chankan:        ctx.Chankan,
		Haitei:         ctx.Haitei,
		Houtei:         ctx.Houtei,
		Tenhou:         ctx.Tenhou,
		Chiihou:        ctx.Chiihou,
		SeatWind:       windToKind(ctx.SeatWind),
		RoundWind:      windToKind(ctx.RoundWind),
		Honba:          ctx.Honba,
		DoraIndicators: ctx.DoraIndicators,
	}
}

// shapeMenzen 报告和牌形是否门清（暗杠不算副露）。
func shapeMenzen(shape rules.WinShape) bool {
	for _, meld := range shape.Melds {
		if meld.Open {
			return false
		}
	}
	return true
}

// windToKind 把 game.Wind 映射成 tile.Kind 的风牌。
func windToKind(wind Wind) tile.Kind {
	switch wind {
	case East:
		return tile.East
	case South:
		return tile.South
	case West:
		return tile.West
	default:
		return tile.North
	}
}

// ── 和牌拆解 ────────────────────────────────────────────────────

// decomposeWin 拆解和牌形：门清 / 副露都走 rules.Decompose（task-13 原生 melds 支持）。
//
// closedHand 必须是 14-3*len(melds) 张（含和了牌）；杠按 4 张放在 melds 里。
func decomposeWin(hand []tile.Tile, melds []Meld, winning tile.Tile) (rules.WinShape, bool) {
	rulesMelds, ok := toRulesMelds(melds)
	if !ok {
		return rules.WinShape{}, false
	}
	return rules.Decompose(hand, rulesMelds, winning)
}

// toRulesMelds 把 game.Meld 转成 rules.Decompose 的副露入参（Kind + Tiles）。
func toRulesMelds(melds []Meld) ([]rules.Meld, bool) {
	out := make([]rules.Meld, 0, len(melds))
	for _, meld := range melds {
		kind, ok := rulesMeldKind(meld.Kind)
		if !ok {
			return nil, false
		}
		out = append(out, rules.Meld{
			Kind:  kind,
			Tiles: append([]tile.Tile(nil), meld.Tiles...),
		})
	}
	return out, true
}

// rulesMeldKind 把 game.MeldKind 映射成 rules 的副露 Kind 字符串。
func rulesMeldKind(kind MeldKind) (string, bool) {
	switch kind {
	case MeldChi:
		return rules.MeldChi, true
	case MeldPon:
		return rules.MeldPon, true
	case MeldKanDaiminkan:
		return rules.MeldKanDaiminkan, true
	case MeldKanShouminkan:
		return rules.MeldKanShouminkan, true
	case MeldKanAnkan:
		return rules.MeldKanAnkan, true
	default:
		return "", false
	}
}
