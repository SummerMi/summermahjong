package agent

import (
	"context"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// DefenseAgent 是「守備」策略：先看放铳风险，再看效率。
//
// 危险度模型（启发式，量纲 0..~100，越大越危险）：
//
//   - 现物：该牌种出现在某个立直者的舍牌里 → 对这名对手危险度为 0（他不能荣和现物）；
//   - 筋：同花色里对方打过「点数 ±3」的牌 → 危险度 ×0.35
//     （筋牌只输给单吊/嵌张/愚形，立直者最怕的良形两面被排除）；
//   - 壁：同花色「点数 ±2」的牌已全部可见 → 危险度 ×0.55
//     （组成两面所需的搭子不存在，只能输给其他形）；
//   - 剩余张数：4 - 可见张数，剩余越少越安全（×0.55..×1.15）；
//   - 中张（4/5/6）比幺九更危险，字牌按剩余张数估算。
//
// 对手未立直时没有可信的听牌信号，危险度记 0，退化为效率优先；
// 立直者多于一人时按平均处理，保持 0..100 量纲。
type DefenseAgent struct {
	style Style
}

// NewDefenseAgent 构造防守策略；style 的零值按 StyleDefense 处理。
func NewDefenseAgent(style Style) *DefenseAgent {
	return &DefenseAgent{style: normalizeStyle(style, StyleDefense)}
}

// Name 实现 Agent。
func (a *DefenseAgent) Name() string { return "defense:" + a.style.Name }

// Decide 实现 Agent：有人立直时按「危险度 → 向听 → 进张」字典序选牌，
// 无人立直时退化为 Style 参数化的效率策略。
func (a *DefenseAgent) Decide(ctx context.Context, obs game.Observation) (game.Action, error) {
	if err := ctx.Err(); err != nil {
		return game.Action{}, err
	}
	if len(obs.Hand) == 0 || len(candidateDiscards(obs)) == 0 {
		return fallbackAction(obs)
	}

	meldCount := ownMeldCount(obs)
	visible := visibleCounts(obs)
	evals := evaluateCandidates(obs, a.style, meldCount, visible)
	if len(evals) == 0 {
		return fallbackAction(obs)
	}

	if hasRiichiOpponent(obs) {
		// 立直宣言后：安全第一。危险度相同时再比向听，最后比进张与牌价值。
		best := evals[0]
		for _, ev := range evals[1:] {
			if lessDangerFirst(ev, best) {
				best = ev
			}
		}
		return game.Action{Kind: game.ActDiscard, Seat: obs.Seat, Tile: best.Tile}, nil
	}

	// 无立直：效率优先（危险度为 0，Style 权重决定向听/进张/牌价值取舍）。
	minS := minShanten(evals)
	for i := range evals {
		if evals[i].Shanten == minS {
			withUkeire(obs, &evals[i], a.style, meldCount, visible)
		}
	}
	best, ok := bestByScore(evals)
	if !ok {
		return fallbackAction(obs)
	}
	return game.Action{Kind: game.ActDiscard, Seat: obs.Seat, Tile: best.Tile}, nil
}

// lessDangerFirst 是「守備」的字典序比较：
// 危险度升序 → 向听升序 → 进张降序 → 保留价值降序。
func lessDangerFirst(a, b scoredDiscard) bool {
	if a.Danger != b.Danger {
		return a.Danger < b.Danger
	}
	if a.Shanten != b.Shanten {
		return a.Shanten < b.Shanten
	}
	if a.Ukeire != b.Ukeire {
		return a.Ukeire > b.Ukeire
	}
	return a.KeepValue > b.KeepValue
}

// hasRiichiOpponent 报告是否有其他家已立直。
func hasRiichiOpponent(obs game.Observation) bool {
	for seat := 0; seat < 4; seat++ {
		if game.Seat(seat) != obs.Seat && obs.Riichi[seat] {
			return true
		}
	}
	return false
}

// dangerScore 返回打出 t 的平均放铳危险度（对所有立直者取平均）。
// 没有立直者时返回 0：M1 没有其他听牌信号，不凭空制造风险。
func dangerScore(obs game.Observation, t tile.Tile, visible counts34) float64 {
	opponents := 0
	sum := 0.0
	for seat := 0; seat < 4; seat++ {
		opp := game.Seat(seat)
		if opp == obs.Seat || !obs.Riichi[seat] {
			continue
		}
		opponents++
		sum += opponentDanger(obs, opp, t, visible)
	}
	if opponents == 0 {
		return 0
	}
	return sum / float64(opponents)
}

// opponentDanger 估计打出 t 对单个立直对手的放铳风险。
func opponentDanger(obs game.Observation, opp game.Seat, t tile.Tile, visible counts34) float64 {
	kind := t.Kind
	if kind >= tile.NumKinds {
		return 0
	}
	// 现物：对手自己打过的牌，不能荣和（同牌种即可）。
	if containsKind(obs.Discards[opp], kind) {
		return 0
	}

	remaining := remainingCopies(visible, kind)
	base := baseDanger(kind, remaining)

	if !kind.IsHonor() {
		// 筋：同花色 ±3 的牌被打过 → 两面良形被排除，只输愚形/单吊/嵌张。
		if discardedOffset(obs.Discards[opp], kind, -3) || discardedOffset(obs.Discards[opp], kind, 3) {
			base *= 0.35
		}
		// 壁：±2 的牌全部可见 → 该侧两面搭子不存在。
		if wallOn(kind, -2, visible) || wallOn(kind, 2, visible) {
			base *= 0.55
		}
	}

	// 剩余张数越少，越不可能在对手手里，也越难被摸到。
	factor := 0.55 + 0.15*float64(remaining)
	if remaining == 0 {
		factor = 0.3
	}
	return base * factor
}

// baseDanger 是按牌型给的基准危险度（未考虑筋/壁/剩余张数）。
func baseDanger(kind tile.Kind, remaining int) float64 {
	if kind.IsHonor() {
		return 8 + 12*float64(remaining)
	}
	switch n := kind.Number(); {
	case n == 1 || n == 9:
		return 22
	case n == 2 || n == 8:
		return 36
	case n == 3 || n == 7:
		return 52
	default: // 4 / 5 / 6
		return 78
	}
}

// containsKind 报告牌堆里是否出现过某种牌。
func containsKind(tiles []tile.Tile, kind tile.Kind) bool {
	for _, t := range tiles {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

// suitedKindAt 返回同花色、指定点数（1..9）的牌种；越界返回 false。
func suitedKindAt(kind tile.Kind, number int) (tile.Kind, bool) {
	if kind.IsHonor() || number < 1 || number > 9 {
		return 0, false
	}
	base := int(kind) - (kind.Number() - 1)
	return tile.Kind(base + number - 1), true
}

// discardedOffset 报告对手舍牌里是否有「同花色、点数为 n+delta」的牌。
func discardedOffset(discards []tile.Tile, kind tile.Kind, delta int) bool {
	target, ok := suitedKindAt(kind, kind.Number()+delta)
	if !ok {
		return false
	}
	return containsKind(discards, target)
}

// wallOn 报告「同花色、点数为 n+delta」的牌是否 4 张全部可见（形成壁）。
func wallOn(kind tile.Kind, delta int, visible counts34) bool {
	target, ok := suitedKindAt(kind, kind.Number()+delta)
	if !ok {
		return false
	}
	return int(visible[target]) >= tile.CopiesPerKind
}
