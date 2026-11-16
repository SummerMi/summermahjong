package agent

import (
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// maybeRiichi 在「同一切牌有立直选项」时把打牌升级为立直。
//
// 背景：S8 的策略层原本只输出打牌动作，规则层虽然完整支持立直，
// 评测里的立直率却长期是 0——这会让「多智能体评测」这张表失去说服力。
// 这里补上最小可用的立直决策：如果向听/进张/防守选出的最优切牌同时有
// 立直选项，就宣言立直（+1 翻 + 裏宝牌机会）；风险取舍已经由 Style
// 的防守权重在切牌阶段体现。
func maybeRiichi(obs game.Observation, tl tile.Tile) game.Action {
	for _, action := range obs.LegalActions {
		if action.Kind == game.ActRiichi && action.Tile == tl {
			return action
		}
	}
	return game.Action{Kind: game.ActDiscard, Seat: obs.Seat, Tile: tl}
}
