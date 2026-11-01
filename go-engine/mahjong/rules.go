package mahjong

// 胡牌判定与听牌计算：34 种牌的定长计数数组 + 递归回溯拆解。
//
// 整体思路与 Python 版 rules.py 完全一致（面试可直接讲）：
//
//  1. 把手牌映射成 [34]uint8 计数数组，下标即 TileNames 顺序。
//     用定长数组而不是 map：没有哈希开销、可整体值拷贝，且「按牌序从小到大扫描」天然可行；
//  2. 标准型 = 4 个面子 + 1 个将牌。先枚举将牌（任一张 ≥ 2 张的牌），扣掉将牌后，
//     从最小的非零牌开始递归拆面子：每步只能选刻子或顺子，顺子必须同花色连续三张，
//     所以字牌（下标 ≥ 27）永远无法组顺子；
//  3. 七对单独判断：所有牌种计数都是偶数且总对数恰为 7（4 张相同算两对）；
//  4. 听牌 = 对 34 种牌逐一试加一张，再走上面的判定；手牌中已有 4 张的牌种跳过。
//
// 与 Python 版的一处 Go 化差异：拆面子时不再「就地修改再手工恢复」计数数组，
// 而是把定长数组按值传进递归（Go 的值语义天然产生副本）。少了一半还原代码，
// 也不会出现忘记恢复导致的别名 bug；34 字节的拷贝成本在这个规模下可以忽略。

// Pattern 是胡牌牌型；PatternNone 表示未胡。
type Pattern string

const (
	// PatternNone 表示未胡牌。
	PatternNone Pattern = ""
	// PatternStandard 表示标准型：4 个面子 + 1 个将牌。
	PatternStandard Pattern = "standard"
	// PatternSevenPairs 表示七对：每种牌偶数张、共 7 对（4 张相同算两对）。
	PatternSevenPairs Pattern = "seven_pairs"
)

// 标准型需要的面子数、七对需要的对数与序数牌种数（前 27 种可组顺子）。
const (
	meldsRequired   = 4
	sevenPairsCount = 7
	suitedKindCount = 27
)

// String 返回牌型名；未胡时返回 "none"，便于日志打印。
func (p Pattern) String() string {
	if p == PatternNone {
		return "none"
	}
	return string(p)
}

// IsWin 报告该牌型是否算胡牌。
func (p Pattern) IsWin() bool {
	return p == PatternStandard || p == PatternSevenPairs
}

// isStandard 判断计数数组是否满足标准型（4 面子 + 1 将）。
// counts 按值传入，调用方不会被修改；内部逐次拷贝给 canFormMelds。
func isStandard(counts *[NumTileKinds]uint8) bool {
	for pair := 0; pair < NumTileKinds; pair++ {
		if counts[pair] < 2 {
			continue
		}
		rest := *counts
		rest[pair] -= 2
		if canFormMelds(rest, 0, meldsRequired) {
			return true
		}
	}
	return false
}

// canFormMelds 判断计数数组能否恰好拆成 meldsLeft 个面子（刻子或顺子）。
//
// counts 按值传递：每个分支拿到的都是一份副本，不需要 Python 版那种「先减、递归、
// 再手工加回去」的还原逻辑。start 是「第一个非零下标」的提示，递归只会把 start
// 往后推，不会回退，因此可以少扫前缀（这是 Python 版没有的廉价优化）。
func canFormMelds(counts [NumTileKinds]uint8, start, meldsLeft int) bool {
	if meldsLeft == 0 {
		// 面子数量刚好用完时，必须一张不剩才算拆解成功。
		return allZeroFrom(counts, start)
	}

	first := -1
	for index := start; index < NumTileKinds; index++ {
		if counts[index] != 0 {
			first = index
			break
		}
	}
	if first < 0 {
		return false
	}

	// 分支一：把最小非零牌当刻子（三张相同）。
	if counts[first] >= 3 {
		next := counts
		next[first] -= 3
		if canFormMelds(next, first, meldsLeft-1) {
			return true
		}
	}

	// 分支二：把最小非零牌当顺子的首张；字牌与跨花色都不允许。
	// first < 27 保证是序数牌；first%9 <= 6 保证 first 最大只能是 7（才能凑 7-8-9），
	// 也就不会跨到下一花色。字牌下标从 27 开始，天然被排除在顺子之外。
	if first < suitedKindCount && first%9 <= 6 && counts[first+1] > 0 && counts[first+2] > 0 {
		next := counts
		next[first]--
		next[first+1]--
		next[first+2]--
		if canFormMelds(next, first, meldsLeft-1) {
			return true
		}
	}

	return false
}

// allZeroFrom 报告从 start 开始的后缀是否全为 0。
func allZeroFrom(counts [NumTileKinds]uint8, start int) bool {
	for index := start; index < NumTileKinds; index++ {
		if counts[index] != 0 {
			return false
		}
	}
	return true
}

// isSevenPairs 判断计数数组是否满足七对。
//
// 14 张牌下「所有计数为偶数且总对数恰为 7」等价于七对；4 张相同算两对，
// 因此「豪华七对」也被认可。
func isSevenPairs(counts *[NumTileKinds]uint8) bool {
	pairs := 0
	for _, count := range counts {
		if count%2 != 0 {
			return false
		}
		pairs += int(count / 2)
	}
	return pairs == sevenPairsCount
}

// WinningPattern 判定 14 张手牌的胡牌牌型（顺序无关）。
//
// 返回 PatternStandard、PatternSevenPairs 或 PatternNone；两者同时成立时优先返回
// 标准型（与 Python 版一致：标准型是更基础的定义，七对只是特殊分支）。
//
// 张数不是 14、含未知牌或同种牌超过 4 张时返回包装了 ErrInvalidHand 的错误。
func WinningPattern(hand []Tile) (Pattern, error) {
	if err := ValidateHand(hand, WinHandSize); err != nil {
		return PatternNone, err
	}
	counts := countValidated(hand)
	if isStandard(&counts) {
		return PatternStandard, nil
	}
	if isSevenPairs(&counts) {
		return PatternSevenPairs, nil
	}
	return PatternNone, nil
}

// IsWinningHand 报告 14 张手牌是否胡牌（标准型或七对）。
//
// 等价于 WinningPattern(hand) 返回非 PatternNone；非法手牌同样返回 ErrInvalidHand。
func IsWinningHand(hand []Tile) (bool, error) {
	pattern, err := WinningPattern(hand)
	if err != nil {
		return false, err
	}
	return pattern.IsWin(), nil
}

// WaitingTiles 计算 13 张手牌的听牌，即「再摸哪些牌就能胡」（顺序无关）。
//
// 返回能胡的牌种列表，按 TileNames 顺序升序；空切片表示不听牌（而不是错误）。
// 候选牌若手牌中已有 4 张则跳过——牌山不存在第 5 张。
// 张数不是 13、含未知牌或同种牌超过 4 张时返回包装了 ErrInvalidHand 的错误。
func WaitingTiles(hand []Tile) ([]Tile, error) {
	if err := ValidateHand(hand, TenpaiHandSize); err != nil {
		return nil, err
	}
	counts := countValidated(hand)
	waits := make([]Tile, 0, 4)
	for tile := Tile(0); tile < NumTileKinds; tile++ {
		if counts[tile] >= TilesPerKind {
			continue
		}
		counts[tile]++
		if isStandard(&counts) || isSevenPairs(&counts) {
			waits = append(waits, tile)
		}
		counts[tile]--
	}
	return waits, nil
}
