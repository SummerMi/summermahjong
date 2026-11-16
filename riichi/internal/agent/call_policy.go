package agent

import (
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// decideResponseAction 是响应窗口的统一策略（2026-11-16 补）：
//
//   - 荣和必取；
//   - 门清 / 守備打风（Style.MeldsBias < 0.5）一律 pass；
//   - 有人立直时 pass（不往危险局面上撞）；
//   - 已经听牌（向听 <= 0）时 pass，留门清立直；
//   - 其余情况只在「鸣牌后向听数严格变好」时鸣（吃 / 碰 / 大明杠）。
//
// 背景：此前响应窗口直接走 fallbackAction（第一个合法动作），等于见招就鸣——
// 200 轮里吃了 1510 次副露，门清手被吃光，立直率被稀释到 2.5%–5.5%。
func decideResponseAction(obs game.Observation, style Style) game.Action {
	for _, action := range obs.Responses {
		if action.Kind == game.ActRon {
			return action
		}
	}
	pass := game.Action{Kind: game.ActPass, Seat: obs.Seat}
	if style.MeldsBias < 0.5 || hasRiichiOpponent(obs) {
		return pass
	}
	meldCount := ownMeldCount(obs)
	current := rules.Shanten(obs.Hand, meldCount)
	if current <= 0 {
		return pass
	}
	for _, action := range obs.Responses {
		switch action.Kind {
		case game.ActPon, game.ActChi, game.ActKanDaiminkan:
			if after, ok := callShanten(obs, action, meldCount); ok && after < current {
				return action
			}
		}
	}
	return pass
}

// callShanten 计算「执行这次鸣牌并打出最优一张之后」的向听数。
//
// 吃/碰：手牌先减掉 Consumed（剩 11-3m 张），再枚举打出一张（10-3m 张 = 13-3(m+1)）；
// 大明杠：减掉 3 张后摸岭上再摸切，最终形恰好是减完之后的 10-3m 张。
func callShanten(obs game.Observation, action game.Action, meldCount int) (int, bool) {
	rest, ok := removeConsumed(obs.Hand, action.Consumed)
	if !ok {
		return 0, false
	}
	if action.Kind == game.ActKanDaiminkan {
		s := rules.Shanten(rest, meldCount+1)
		return s, s != rules.ShantenInvalid
	}
	best := 99
	seen := make(map[tile.Kind]bool, len(rest))
	for _, tl := range rest {
		if seen[tl.Kind] {
			continue
		}
		seen[tl.Kind] = true
		after, ok := removeOneKind(rest, tl.Kind)
		if !ok {
			continue
		}
		if s := rules.Shanten(after, meldCount+1); s != rules.ShantenInvalid && s < best {
			best = s
		}
	}
	return best, best != 99
}

// removeConsumed 从手牌里依次移除被鸣掉的牌。
func removeConsumed(hand []tile.Tile, consumed []tile.Tile) ([]tile.Tile, bool) {
	out := append([]tile.Tile(nil), hand...)
	for _, c := range consumed {
		var ok bool
		out, ok = removeOneKind(out, c.Kind)
		if !ok {
			return nil, false
		}
	}
	return out, true
}

// removeOneKind 从手牌中移除一张指定牌种。
func removeOneKind(hand []tile.Tile, kind tile.Kind) ([]tile.Tile, bool) {
	for i, tl := range hand {
		if tl.Kind == kind {
			out := make([]tile.Tile, 0, len(hand)-1)
			out = append(out, hand[:i]...)
			out = append(out, hand[i+1:]...)
			return out, true
		}
	}
	return nil, false
}
