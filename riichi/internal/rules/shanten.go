package rules

import "summermahjong/riichi/internal/tile"

// ShantenInvalid 是 Shanten 对非法输入的返回值：牌面非法（未知牌 / 同种超过 4 张），
// 或张数不是听牌形（len(hand)+3*meldCount 必须等于 13）。
const ShantenInvalid = -2

// Shanten 计算向听数：0 表示听牌，n>0 表示 n 向听；数值越小越接近和牌。
//
// 使用默认规则（日麻严格七对语义）；v1 兼容语义请用 ShantenWithOptions(..., CompatOptions())。
//
// 参数约定：
//   - hand：门清手牌（不含副露的牌），张数必须满足 len(hand) + 3*meldCount == 13，
//     即「13 张听牌形」：meldCount=0 时是 13 张，有 1 组副露时是 10 张，依此类推。
//     摸牌后的 14 张形态请先打出一张再调用；判胡请用 Decompose（14 张 + 和了牌）。
//   - meldCount：已副露的面子数（0..4）。杠按 1 组面子计（杠多出的那张已从手牌扣除），
//     因此上面的张数公式对带杠的手牌同样成立。
//
// 返回值：门清 13 张手牌的向听数在 0..6 之间；非法输入返回 ShantenInvalid(-2)。
//
// 算法：分别计算「标准型 / 七对子 / 国士无双」的向听数并取最小值，与常见日麻引擎一致。
// 副露后（meldCount>0）七对与国士不可能成立，只算标准型。
func Shanten(hand []tile.Tile, meldCount int) int {
	return ShantenWithOptions(hand, meldCount, Options{})
}

// ShantenWithOptions 是带规则选项的 Shanten（目前只影响七对子的向听公式）。
func ShantenWithOptions(hand []tile.Tile, meldCount int, opts Options) int {
	if meldCount < 0 || meldCount > meldsRequired {
		return ShantenInvalid
	}
	c, ok := countTiles(hand)
	if !ok {
		return ShantenInvalid
	}
	if len(hand)+3*meldCount != tenpaiHandSize {
		return ShantenInvalid
	}

	best := shantenStandard(c, meldCount)
	if meldCount == 0 {
		if s := shantenChiitoi(c, !opts.StrictSevenPairs()); s < best {
			best = s
		}
		if s := shantenKokushi(c); s < best {
			best = s
		}
	}
	return best
}

// shantenStandard 计算标准型（4 面子 + 1 将）的向听数。
//
// 经典公式：向听数 = 8 - 2*面子 - 搭子 - 雀头，其中「搭子」按 4-面子 截断。
// 这里用 DFS 枚举手牌的全部「面子 / 搭子 / 雀头」拆法，取公式最小值：
// 每次固定处理最小非零牌，尝试刻子、顺子、对子（作雀头或作搭子）、
// 两面/边张搭子、嵌张搭子，最后允许把这张当孤张丢掉（相当于之后打出）。
func shantenStandard(c counts, meldCount int) int {
	best := 8 // 理论最大值：0 面子 0 搭子 0 雀头
	var dfs func(c counts, idx, melds, taatsu, jantou int)
	dfs = func(c counts, idx, melds, taatsu, jantou int) {
		m := meldCount + melds
		t := taatsu
		if limit := meldsRequired - m; t > limit {
			t = limit
		}
		if t < 0 {
			t = 0
		}
		if sh := 8 - 2*m - t - jantou; sh < best {
			best = sh
		}

		for idx < tile.NumKinds && c[idx] == 0 {
			idx++
		}
		if idx >= tile.NumKinds {
			return
		}

		// 面子分支：已经凑满 4 组就只剩搭子/雀头可算。
		if m < meldsRequired {
			if c[idx] >= 3 {
				next := c
				next[idx] -= 3
				dfs(next, idx, melds+1, taatsu, jantou)
			}
			if isSuitedIndex(idx) && idx%9 <= 6 && c[idx+1] > 0 && c[idx+2] > 0 {
				next := c
				next[idx]--
				next[idx+1]--
				next[idx+2]--
				dfs(next, idx, melds+1, taatsu, jantou)
			}
		}

		// 对子分支：可作雀头，也可作「等第三张成刻」的搭子。
		if c[idx] >= 2 {
			next := c
			next[idx] -= 2
			if jantou == 0 {
				dfs(next, idx, melds, taatsu, 1)
			}
			dfs(next, idx, melds, taatsu+1, jantou)
		}

		// 两面 / 边张搭子：同花色相邻两张（12、23、…、89）。
		if isSuitedIndex(idx) && idx%9 <= 7 && c[idx+1] > 0 {
			next := c
			next[idx]--
			next[idx+1]--
			dfs(next, idx, melds, taatsu+1, jantou)
		}

		// 嵌张搭子：同花色隔一张（13、24、…、79）。
		if isSuitedIndex(idx) && idx%9 <= 6 && c[idx+2] > 0 {
			next := c
			next[idx]--
			next[idx+2]--
			dfs(next, idx, melds, taatsu+1, jantou)
		}

		// 孤张分支：这张不参与任何面子/搭子，相当于之后会打出。
		next := c
		next[idx]--
		dfs(next, idx, melds, taatsu, jantou)
	}

	dfs(c, 0, 0, 0, 0)
	return best
}

// shantenChiitoi 计算七对子的向听数。
//
// compat=false（日麻严格）：七对需要 7 种不同的对子。记 P 为「已有对子的种数」
// （4 张相同只算 1 对）、K 为不同牌种数，则向听数 = 6 - P + max(0, 7 - K)。
//
// compat=true（v1 中国规则）：4 张相同算两对，记 P 为总对数（count/2 之和），
// 13 张牌下 P 最大为 6，向听数 = 6 - P。
func shantenChiitoi(c counts, compat bool) int {
	if compat {
		pairs := 0
		for _, n := range c {
			pairs += int(n / 2)
		}
		return 6 - pairs
	}

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

// shantenKokushi 计算国士无双的向听数。
//
// 记 D 为已经出现的幺九字牌种数、P 为其中是否有对子：
// 向听数 = 13 - D - (P ? 1 : 0)。13 张牌下最多 13 种，故结果不小于 0。
func shantenKokushi(c counts) int {
	distinct := 0
	hasPair := false
	for i := 0; i < tile.NumKinds; i++ {
		if !isTerminalOrHonor(i) {
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
