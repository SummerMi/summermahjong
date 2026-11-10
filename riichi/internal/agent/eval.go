package agent

import (
	"sort"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/tile"
)

// 与 rules 包保持一致的常量（agent 只读规则，不复制规则逻辑到 game）。
const (
	meldsRequired   = 4
	suitedKindCount = 27
	sevenPairsCount = 7
	kokushiKinds    = 13
	// shantenInvalid 是 rules.Shanten 的非法输入返回值，这里单独命名便于读代码。
	shantenInvalid = rules.ShantenInvalid
)

// counts34 是 34 种牌的计数数组（下标即 tile.Kind）。
type counts34 [tile.NumKinds]uint8

// countsOf 统计牌面；越界牌忽略（Observation 里不应出现，防御性处理）。
func countsOf(tiles []tile.Tile) counts34 {
	var c counts34
	for _, t := range tiles {
		if t.Kind < tile.NumKinds {
			c[t.Kind]++
		}
	}
	return c
}

// shantenAny 计算手牌向听数：-1 表示和牌形，0 表示听牌，n 表示 n 向听。
//
// 与 rules.Shanten 的区别：rules 只接受「13 - 3*meldCount」张的听牌形；
// 这里允许「摸牌后的 14 - 3*meldCount 张」形态，返回值等价于
// 「打出最优一张之后的向听数」。这样在「候选切牌 × 进张」的两层枚举里，
// 一次 DFS 就能同时完成「摸到这张后打哪张最优」，不必嵌套调用 14 次 Shanten。
//
// 算法与 rules 包的标准型 DFS 相同（面子 / 搭子 / 雀头 + 8-2m-t-j 公式），
// 另外叠了七对与国士；14 张形态下 4 面子 + 1 将恰好用满 14 张，返回 -1。
func shantenAny(c counts34, meldCount int) int {
	if meldCount < 0 || meldCount > meldsRequired {
		return shantenInvalid
	}
	best := 8
	shantenStandard(c, meldCount, &best)
	if meldCount == 0 {
		if s := chiitoiShanten(c); s < best {
			best = s
		}
		if s := kokushiShanten(c); s < best {
			best = s
		}
	}
	return best
}

// shantenStandard 用 DFS 枚举面子/搭子/雀头，取 8-2m-t-j 的最小值。
//
// 尾部分支「孤张」相当于「这张之后会被打出」，所以对 14 张形态同样适用。
func shantenStandard(c counts34, meldCount int, best *int) {
	var dfs func(c counts34, idx, melds, taatsu, jantou int)
	dfs = func(c counts34, idx, melds, taatsu, jantou int) {
		m := meldCount + melds
		t := taatsu
		if limit := meldsRequired - m; t > limit {
			t = limit
		}
		if t < 0 {
			t = 0
		}
		if sh := 8 - 2*m - t - jantou; sh < *best {
			*best = sh
		}

		for idx < tile.NumKinds && c[idx] == 0 {
			idx++
		}
		if idx >= tile.NumKinds {
			return
		}

		// 面子：刻子 / 顺子。
		if m < meldsRequired {
			if c[idx] >= 3 {
				next := c
				next[idx] -= 3
				dfs(next, idx, melds+1, taatsu, jantou)
			}
			if idx < suitedKindCount && idx%9 <= 6 && c[idx+1] > 0 && c[idx+2] > 0 {
				next := c
				next[idx]--
				next[idx+1]--
				next[idx+2]--
				dfs(next, idx, melds+1, taatsu, jantou)
			}
		}

		// 对子：可作雀头，也可作「等第三张」的搭子。
		if c[idx] >= 2 {
			next := c
			next[idx] -= 2
			if jantou == 0 {
				dfs(next, idx, melds, taatsu, 1)
			}
			dfs(next, idx, melds, taatsu+1, jantou)
		}

		// 两面 / 边张搭子。
		if idx < suitedKindCount && idx%9 <= 7 && c[idx+1] > 0 {
			next := c
			next[idx]--
			next[idx+1]--
			dfs(next, idx, melds, taatsu+1, jantou)
		}

		// 嵌张搭子。
		if idx < suitedKindCount && idx%9 <= 6 && c[idx+2] > 0 {
			next := c
			next[idx]--
			next[idx+2]--
			dfs(next, idx, melds, taatsu+1, jantou)
		}

		// 孤张：不参与任何面子/搭子（之后打出）。
		next := c
		next[idx]--
		dfs(next, idx, melds, taatsu, jantou)
	}
	dfs(c, 0, 0, 0, 0)
}

// chiitoiShanten 计算七对子向听数，与 rules.Shanten 的默认语义（日麻严格）一致：
// 4 张相同只算 1 对，且必须凑满 7 种不同的对子。
//
// pairs = 有 ≥2 张的牌种数，kinds = 出现过的牌种数，则
// 向听数 = 6 - pairs + max(0, 7 - kinds)；14 张恰好 7 对时返回 -1（和牌形）。
func chiitoiShanten(c counts34) int {
	pairs := 0
	kinds := 0
	for _, n := range c {
		if n == 0 {
			continue
		}
		kinds++
		if n >= 2 {
			pairs++
		}
	}
	shanten := 6 - pairs
	if kinds < sevenPairsCount {
		shanten += sevenPairsCount - kinds
	}
	return shanten
}

// kokushiShanten 计算国士无双向听数（13 张听牌形与 14 张形态通用）。
func kokushiShanten(c counts34) int {
	distinct := 0
	hasPair := false
	for i := 0; i < tile.NumKinds; i++ {
		if !tile.Kind(i).IsTerminalOrHonor() {
			continue
		}
		if c[i] > 0 {
			distinct++
		}
		if c[i] >= 2 {
			hasPair = true
		}
	}
	shanten := kokushiKinds - distinct
	if hasPair {
		shanten--
	}
	return shanten
}

// shanten13 计算 13-3*meldCount 张手牌的向听数（以 rules.Shanten 为准）。
func shanten13(hand []tile.Tile, meldCount int) int {
	return rules.Shanten(hand, meldCount)
}

// removeTile 返回去掉一张指定牌后的新手牌（不修改入参）。
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

// candidateDiscards 从合法动作里取出「可打出的不同牌面」，保持原顺序。
//
// M1 只有摸打；M2 的吃/碰/杠/立直/自摸等动作现在不会出现，
// 这里显式过滤 ActDiscard，遇到未知动作交给 fallbackAction 处理。
func candidateDiscards(obs game.Observation) []tile.Tile {
	seen := make(map[tile.Tile]struct{}, len(obs.LegalActions))
	out := make([]tile.Tile, 0, len(obs.LegalActions))
	for _, a := range obs.LegalActions {
		if a.Kind != game.ActDiscard {
			continue
		}
		if _, ok := seen[a.Tile]; ok {
			continue
		}
		seen[a.Tile] = struct{}{}
		out = append(out, a.Tile)
	}
	return out
}

// fallbackAction 在没有可评估的打牌候选时给出一个合法动作。
//
// 优先级预留给未来的动作：和了 > 摸牌 > 第一个合法动作。
// 这样 M2 加入自摸/荣和后，即使估值器暂时不认识这些动作也不会漏和。
func fallbackAction(obs game.Observation) (game.Action, error) {
	if len(obs.LegalActions) == 0 {
		return game.Action{}, ErrNoLegalAction
	}
	for _, prefer := range [...]game.ActionKind{game.ActTsumo, game.ActRon, game.ActDraw} {
		for _, a := range obs.LegalActions {
			if a.Kind == prefer {
				return a, nil
			}
		}
	}
	return obs.LegalActions[0], nil
}

// ownMeldCount 返回自己的副露组数（杠按 1 组面子计，与 rules.Shanten 约定一致）。
func ownMeldCount(obs game.Observation) int {
	if int(obs.Seat) >= len(obs.Melds) {
		return 0
	}
	return len(obs.Melds[obs.Seat])
}

// visibleCounts 统计全场可见的牌：自己的手牌 + 四家舍牌 + 副露 + 宝牌指示牌。
//
// 用于估算「某张牌还剩几张可能被摸到/留在对手手里」。越界 Kind 忽略。
func visibleCounts(obs game.Observation) counts34 {
	var v counts34
	add := func(tiles []tile.Tile) {
		for _, t := range tiles {
			if t.Kind < tile.NumKinds {
				v[t.Kind]++
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
	return v
}

// remainingCopies 返回某牌种在「未可见区域」里还剩几张（0..4）。
func remainingCopies(visible counts34, kind tile.Kind) int {
	if kind >= tile.NumKinds {
		return 0
	}
	left := tile.CopiesPerKind - int(visible[kind])
	if left < 0 {
		return 0
	}
	return left
}

// ukeire 计算 13-3*meldCount 张手牌的有效进张数（按剩余张数加权）。
//
// 对可能让向听下降的牌各试加一张，用 shantenAny 判断是否真的下降；
// 返回「还能摸到的张数总和」，不是牌种数。
func ukeire(hand []tile.Tile, meldCount, baseShanten int, visible counts34) int {
	uk, _ := analyzeAfter(hand, meldCount, baseShanten, visible)
	return uk
}

// analyzeAfter 一次遍历同时算出「有效进张」与「摸一张后的期望向听」。
//
// 两个量都需要对候选进张各跑一次 14 张形态 DFS，合并后搜索策略只付一次成本。
// 关键剪枝：只有与手牌中某张牌同种、或同花色距离 ≤2 的牌种才可能让向听下降
// （一张牌要参与新的面子/搭子/对子，必须和现有牌相邻），其余 34 种里的牌
// 摸到后最优选择就是原样打出、向听保持 baseShanten，直接按权重并入期望即可。
func analyzeAfter(after []tile.Tile, meldCount, baseShanten int, visible counts34) (ukeire int, expected float64) {
	if baseShanten == shantenInvalid {
		return 0, 0
	}
	c := countsOf(after)

	totalRemaining := 0
	for kind := 0; kind < tile.NumKinds; kind++ {
		totalRemaining += remainingCopies(visible, tile.Kind(kind))
	}
	if totalRemaining == 0 {
		return 0, float64(baseShanten)
	}

	interestingRemaining := 0
	weighted := 0.0
	for _, kind := range interestingDrawKinds(c, meldCount) {
		rem := remainingCopies(visible, tile.Kind(kind))
		if rem == 0 {
			continue
		}
		next := c
		next[kind]++
		s := shantenAny(next, meldCount)
		if s == shantenInvalid {
			continue
		}
		interestingRemaining += rem
		weighted += float64(rem) * float64(s)
		if s < baseShanten {
			ukeire += rem
		}
	}

	// 其余进张不改变向听：摸到后原样打出，保持 baseShanten。
	restRemaining := totalRemaining - interestingRemaining
	expected = (weighted + float64(restRemaining)*float64(baseShanten)) / float64(totalRemaining)
	return ukeire, expected
}

// interestingDrawKinds 返回「可能改变向听」的进张牌种（升序）：
//   - 与手牌某张牌同种，或同花色点数差 1..2 的牌种（可能成对/成搭/成顺）；
//   - 副露数为 0 时，全部幺九字牌（可能推进国士无双）。
//
// 字牌只有同种才算候选（不能组顺子）。
//
// 这是安全的剪枝：一张既不与现有牌相邻、也不是幺九字牌的牌，摸到后下一步
// 只能把它打出，向听不可能比现在更小。
func interestingDrawKinds(c counts34, meldCount int) []int {
	var marked [tile.NumKinds]bool
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] == 0 {
			continue
		}
		marked[kind] = true
		if kind >= suitedKindCount {
			continue // 字牌不能组顺子，只可能同种成对/成刻
		}
		rank := kind % 9
		for delta := -2; delta <= 2; delta++ {
			next := rank + delta
			if next < 0 || next > 8 {
				continue
			}
			marked[kind-rank+next] = true
		}
	}
	// 国士无双只需要幺九字牌；有副露后不再可能成立。
	if meldCount == 0 {
		for kind := 0; kind < tile.NumKinds; kind++ {
			if tile.Kind(kind).IsTerminalOrHonor() {
				marked[kind] = true
			}
		}
	}
	out := make([]int, 0, tile.NumKinds)
	for kind := 0; kind < tile.NumKinds; kind++ {
		if marked[kind] {
			out = append(out, kind)
		}
	}
	return out
}

// scoredDiscard 是一张候选切牌的一次估值结果。
type scoredDiscard struct {
	Tile      tile.Tile
	Shanten   int     // 切掉后的向听数
	Ukeire    int     // 有效进张（张数）
	Danger    float64 // 放铳危险度（0 安全）
	KeepValue float64 // 被切牌的保留价值
	Score     float64 // 综合评分（越低越好）
}

// finalScore 把一次估值折算成可比较的分数。
//
// 约定：向听与进张是「效率」，Danger 是「安全」，KeepValue 是「牌价值」。
// 不同 Style 只改权重，不改公式，保证三种打风可解释、可复现。
func finalScore(ev scoredDiscard, style Style) float64 {
	return float64(ev.Shanten)*style.ShantenWeight -
		float64(ev.Ukeire)*style.UkeireWeight -
		(maxKeepValue-ev.KeepValue)*style.KeepValueWeight +
		ev.Danger*style.DangerWeight
}

// evaluateCandidates 对全部候选切牌做「向听 + 危险度 + 牌价值」估值。
//
// 进张（Ukeire）默认不在这里算：它要对 34 种牌各跑一次 shantenAny，
// 只在真正接近最优的少数候选上计算，避免 14×34 次 DFS 把单步拖到毫秒级。
func evaluateCandidates(obs game.Observation, style Style, meldCount int, visible counts34) []scoredDiscard {
	cands := candidateDiscards(obs)
	if len(cands) == 0 {
		return nil
	}
	out := make([]scoredDiscard, 0, len(cands))
	for _, d := range cands {
		after, ok := removeTile(obs.Hand, d)
		if !ok {
			continue
		}
		s := shanten13(after, meldCount)
		if s == shantenInvalid {
			continue
		}
		ev := scoredDiscard{
			Tile:      d,
			Shanten:   s,
			Danger:    dangerScore(obs, d, visible),
			KeepValue: keepValue(d.Kind, style),
		}
		ev.Score = finalScore(ev, style)
		out = append(out, ev)
	}
	return out
}

// withUkeire 给一张候选补上进张数并刷新综合分。
func withUkeire(obs game.Observation, ev *scoredDiscard, style Style, meldCount int, visible counts34) {
	after, ok := removeTile(obs.Hand, ev.Tile)
	if !ok {
		return
	}
	ev.Ukeire = ukeire(after, meldCount, ev.Shanten, visible)
	ev.Score = finalScore(*ev, style)
}

// minShanten 返回一组候选里的最小向听数；空集合返回 shantenInvalid。
func minShanten(evals []scoredDiscard) int {
	best := shantenInvalid
	for _, ev := range evals {
		if best == shantenInvalid || ev.Shanten < best {
			best = ev.Shanten
		}
	}
	return best
}

// bestByScore 返回综合分最低的候选（分数相同时保留候选顺序，保证确定性）。
func bestByScore(evals []scoredDiscard) (scoredDiscard, bool) {
	if len(evals) == 0 {
		return scoredDiscard{}, false
	}
	best := evals[0]
	for _, ev := range evals[1:] {
		if ev.Score < best.Score {
			best = ev
		}
	}
	return best, true
}

// sortByShantenThenDanger 按「向听升序、危险度升序」稳定排序（搜索的 beam 顺序）。
func sortByShantenThenDanger(evals []scoredDiscard) {
	sort.SliceStable(evals, func(i, j int) bool {
		if evals[i].Shanten != evals[j].Shanten {
			return evals[i].Shanten < evals[j].Shanten
		}
		return evals[i].Danger < evals[j].Danger
	})
}
