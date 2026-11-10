package llm

import (
	"sort"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// Candidate 是一张候选切牌 + 本地规则算好的统计，交给 LLM 做最终选择。
//
// LLM 只负责「在候选里选」，向听 / 进张 / 危险度 / 打点潜力全部由本地计算，
// 因此模型输出错误也不会让引擎拿到非法或明显糟糕的动作（解析失败还有回退）。
type Candidate struct {
	Tile      string  `json:"tile"`
	Shanten   int     `json:"shanten"`   // 切出后向听数（越小越好）
	Ukeire    int     `json:"ukeire"`    // 有效进张张数（越大越好）
	Danger    float64 `json:"danger"`    // 对已立直对手的平均危险度（0..100，越小越好）
	Potential int     `json:"potential"` // 打点潜力：和牌时最大翻数（含宝牌估算）
	Score     float64 `json:"score"`     // 本地综合分（展示用，越大越好）
}

// visibleCountsOf 统计全场可见牌：自己手牌 + 四家舍牌 + 四家副露 + 宝牌指示牌。
func visibleCountsOf(obs game.Observation) [tile.NumKinds]uint8 {
	var visible [tile.NumKinds]uint8
	add := func(tiles []tile.Tile) {
		for _, t := range tiles {
			if t.Kind < tile.NumKinds {
				visible[t.Kind]++
			}
		}
	}
	add(obs.Hand)
	for seat := 0; seat < 4; seat++ {
		add(obs.Discards[seat])
		for _, meld := range obs.Melds[seat] {
			add(meld.Tiles)
		}
	}
	add(obs.DoraIndicators)
	return visible
}

// meldCountOf 返回自己的副露组数（杠按 1 组面子计，与 rules.Shanten 约定一致）。
func meldCountOf(obs game.Observation) int {
	if int(obs.Seat) >= len(obs.Melds) {
		return 0
	}
	return len(obs.Melds[obs.Seat])
}

// remainingCopies 返回某牌种在未可见区域还剩几张。
func remainingCopies(visible [tile.NumKinds]uint8, kind tile.Kind) int {
	if kind >= tile.NumKinds {
		return 0
	}
	left := tile.CopiesPerKind - int(visible[kind])
	if left < 0 {
		return 0
	}
	return left
}

// candidateDiscards 从合法动作里取出不同的「打牌」候选，保持原顺序。
func candidateDiscards(obs game.Observation) []tile.Tile {
	seen := make(map[tile.Tile]struct{}, len(obs.LegalActions))
	out := make([]tile.Tile, 0, len(obs.LegalActions))
	for _, action := range obs.LegalActions {
		if action.Kind != game.ActDiscard {
			continue
		}
		if _, ok := seen[action.Tile]; ok {
			continue
		}
		seen[action.Tile] = struct{}{}
		out = append(out, action.Tile)
	}
	return out
}

// removeTile 返回去掉第一张 target 后的手牌（不修改入参）。
func removeTile(hand []tile.Tile, target tile.Tile) ([]tile.Tile, bool) {
	for i, t := range hand {
		if t != target {
			continue
		}
		out := make([]tile.Tile, 0, len(hand)-1)
		out = append(out, hand[:i]...)
		out = append(out, hand[i+1:]...)
		return out, true
	}
	return nil, false
}

// ukeireAfter 计算 13-3*meldCount 张手牌的有效进张张数（按剩余枚数加权）。
//
// 有效 = 摸到后能让向听下降，或（门清时）直接构成和牌形。
// 注意：rules.Shanten 对 13 张听牌形只返回 0、不会返回 -1，所以和牌张要单独用
// Decompose 判断；副露手牌（meldCount>0）暂时只按「向听下降」统计（和牌张会漏报）。
func ukeireAfter(hand []tile.Tile, meldCount, baseShanten int, visible [tile.NumKinds]uint8) int {
	if baseShanten == rules.ShantenInvalid {
		return 0
	}
	total := 0
	for kind := tile.Kind(0); kind < tile.NumKinds; kind++ {
		remaining := remainingCopies(visible, kind)
		if remaining == 0 {
			continue
		}
		after := make([]tile.Tile, 0, len(hand)+1)
		after = append(after, hand...)
		after = append(after, tile.Tile{Kind: kind})
		if meldCount == 0 && len(after) == 14 && isWinningTiles(after) {
			total += remaining
			continue
		}
		for i := range after {
			rest := make([]tile.Tile, 0, len(after)-1)
			rest = append(rest, after[:i]...)
			rest = append(rest, after[i+1:]...)
			if rules.Shanten(rest, meldCount) < baseShanten {
				total += remaining
				break
			}
		}
	}
	return total
}

// isWinningTiles 报告一组 14 张门清手牌是否已经和牌。
//
// rules.Decompose 要求「hand 含 winning」，所以对出现过的每种牌都试一次拆解；
// 任意一种成立即说明这手牌是和牌形。
func isWinningTiles(hand14 []tile.Tile) bool {
	seen := make(map[tile.Kind]struct{}, len(hand14))
	for _, handTile := range hand14 {
		if _, ok := seen[handTile.Kind]; ok {
			continue
		}
		seen[handTile.Kind] = struct{}{}
		if _, ok := rules.Decompose(hand14, nil, handTile); ok {
			return true
		}
	}
	return false
}

// dangerScore 对每个已立直对手估算危险度并取平均；无人立直时为 0。
//
// 启发式与 internal/agent 的 DefenseAgent 相同（那套实现未导出，LLM 包独立实现）：
// 现物 0；筋 ×0.35；壁 ×0.55；剩余张数越少越安全；中张比幺九/字牌危险。
func dangerScore(obs game.Observation, kind tile.Kind, visible [tile.NumKinds]uint8) float64 {
	opponents := 0
	sum := 0.0
	for seat := 0; seat < 4; seat++ {
		if game.Seat(seat) == obs.Seat || !obs.Riichi[seat] {
			continue
		}
		opponents++
		sum += opponentDanger(obs.Discards[seat], kind, visible)
	}
	if opponents == 0 {
		return 0
	}
	return sum / float64(opponents)
}

// opponentDanger 估计打出某牌种对单个立直对手的放铳危险度。
func opponentDanger(discards []tile.Tile, kind tile.Kind, visible [tile.NumKinds]uint8) float64 {
	if kind >= tile.NumKinds {
		return 0
	}
	if containsKind(discards, kind) {
		return 0 // 现物：对手不能荣和自己打过的牌
	}
	remaining := remainingCopies(visible, kind)
	base := baseDanger(kind, remaining)
	if !kind.IsHonor() {
		if discardedOffset(discards, kind, -3) || discardedOffset(discards, kind, 3) {
			base *= 0.35
		}
		if wallOn(kind, -2, visible) || wallOn(kind, 2, visible) {
			base *= 0.55
		}
	}
	factor := 0.55 + 0.15*float64(remaining)
	if remaining == 0 {
		factor = 0.3
	}
	return base * factor
}

// baseDanger 是按牌型给的基准危险度。
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
	default:
		return 78
	}
}

// maxPotentialHan 估算「当前 13 张手牌若和牌」的最大翻数（含宝牌/赤宝牌）。
//
// 只对门清有效（rules.Decompose 当前只支持门清）；非门清返回 0。
// 做法：对 34 种牌各试一次和了拆解 + Evaluate，取 han+dora 最大值。
// 这个量让 LLM 知道「留哪些牌能打出更高的点数」，而不是只看向听。
func maxPotentialHan(obs game.Observation, hand []tile.Tile, meldCount int) int {
	if meldCount != 0 {
		return 0
	}
	ctx := rules.ScoreContext{
		Menzen:         true,
		SeatWind:       tile.East,
		RoundWind:      tile.East + tile.Kind(obs.RoundWind),
		DoraIndicators: obs.DoraIndicators,
	}
	best := 0
	for kind := tile.Kind(0); kind < tile.NumKinds; kind++ {
		winning := tile.Tile{Kind: kind}
		tiles14 := make([]tile.Tile, 0, len(hand)+1)
		tiles14 = append(tiles14, hand...)
		tiles14 = append(tiles14, winning)
		shape, ok := rules.Decompose(tiles14, nil, winning)
		if !ok {
			continue
		}
		ctx.AkaDora = rules.CountAka(shape)
		_, han, _, err := rules.Evaluate(shape, ctx)
		if err != nil {
			continue
		}
		if total := han + rules.CountDora(shape, ctx); total > best {
			best = total
		}
	}
	return best
}

// evaluateCandidates 对全部候选切牌计算 向听 / 进张 / 危险度。
func evaluateCandidates(obs game.Observation) []Candidate {
	cands := candidateDiscards(obs)
	if len(cands) == 0 {
		return nil
	}
	meldCount := meldCountOf(obs)
	visible := visibleCountsOf(obs)
	out := make([]Candidate, 0, len(cands))
	for _, discard := range cands {
		after, ok := removeTile(obs.Hand, discard)
		if !ok || len(after)+3*meldCount != 13 {
			continue
		}
		shanten := rules.Shanten(after, meldCount)
		if shanten == rules.ShantenInvalid {
			continue
		}
		out = append(out, Candidate{
			Tile:    discard.String(),
			Shanten: shanten,
			Ukeire:  ukeireAfter(after, meldCount, shanten, visible),
			Danger:  round1(dangerScore(obs, discard.Kind, visible)),
		})
	}
	// 效率维度排序：向听升序 → 进张降序 → 危险度升序 → 牌面稳定序。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Shanten != out[j].Shanten {
			return out[i].Shanten < out[j].Shanten
		}
		if out[i].Ukeire != out[j].Ukeire {
			return out[i].Ukeire > out[j].Ukeire
		}
		if out[i].Danger != out[j].Danger {
			return out[i].Danger < out[j].Danger
		}
		return out[i].Tile < out[j].Tile
	})
	return out
}

// TopCandidates 返回 top-k 候选切牌（含打点潜力与综合分），按综合分降序。
//
// 计算顺序：先只算便宜的 向听/进张/危险度 并排序取前 k，再对前 k 张算打点潜力
// （Potential 需要对 34 种和了牌各做一次 Decompose+Evaluate，较贵，不能对全部候选算）。
func TopCandidates(obs game.Observation, k int) []Candidate {
	if k <= 0 {
		return nil
	}
	candidates := evaluateCandidates(obs)
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	meldCount := meldCountOf(obs)

	// 回填打点潜力：需要「切出这张后的手牌」，从牌面文本还原。
	for i := range candidates {
		parsed, err := tile.ParseTile(candidates[i].Tile)
		if err != nil {
			continue
		}
		after, ok := removeTile(obs.Hand, parsed)
		if !ok {
			continue
		}
		candidates[i].Potential = maxPotentialHan(obs, after, meldCount)
		candidates[i].Score = round1(
			float64(candidates[i].Ukeire) -
				float64(candidates[i].Shanten)*10 -
				candidates[i].Danger +
				float64(candidates[i].Potential)*2,
		)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].Shanten != candidates[j].Shanten {
			return candidates[i].Shanten < candidates[j].Shanten
		}
		return candidates[i].Tile < candidates[j].Tile
	})
	return candidates
}

// containsKind 报告一组牌里是否出现过某牌种。
func containsKind(tiles []tile.Tile, kind tile.Kind) bool {
	for _, t := range tiles {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

// suitedKindAt 返回同花色、指定点数的牌种；字牌/越界返回 false。
func suitedKindAt(kind tile.Kind, number int) (tile.Kind, bool) {
	if kind.IsHonor() || number < 1 || number > 9 {
		return 0, false
	}
	base := int(kind) - (kind.Number() - 1)
	return tile.Kind(base + number - 1), true
}

// discardedOffset 报告舍牌里是否有「同花色、点数为 n+delta」的牌。
func discardedOffset(discards []tile.Tile, kind tile.Kind, delta int) bool {
	target, ok := suitedKindAt(kind, kind.Number()+delta)
	if !ok {
		return false
	}
	return containsKind(discards, target)
}

// wallOn 报告「同花色、点数为 n+delta」的牌是否 4 张全部可见（壁）。
func wallOn(kind tile.Kind, delta int, visible [tile.NumKinds]uint8) bool {
	target, ok := suitedKindAt(kind, kind.Number()+delta)
	if !ok {
		return false
	}
	return int(visible[target]) >= tile.CopiesPerKind
}

// round1 保留 1 位小数（prompt 里更好读）。
func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
