package rules

import "summermahjong/riichi/internal/tile"

// 规则计算用到的固定常量。
const (
	// winHandSize 是和牌拆解要求的物理张数（门清 14 张）。
	winHandSize = 14
	// tenpaiHandSize 是向听计算要求的物理张数（门清 13 张）。
	tenpaiHandSize = 13
	// meldsRequired 是标准型需要的面子数。
	meldsRequired = 4
	// sevenPairsCount 是七对子需要的对数。
	sevenPairsCount = 7
	// kokushiKinds 是国士无双需要的幺九字牌种数。
	kokushiKinds = 13
	// suitedKindCount 是序数牌种数（万/筒/索各 9 种，下标 0..26）；其后 27..33 为字牌。
	suitedKindCount = 27
)

// counts 是 34 种牌的计数数组，下标即 tile.Kind；比 map 省内存、可整体值拷贝。
type counts [tile.NumKinds]uint8

// countTiles 统计牌面。
//
// ok=false 表示牌面非法：Kind 越界（未知牌）或同种牌超过 4 张。
// 赤宝牌（Tile.Aka）与普通牌同 Kind，计入同一计数。
func countTiles(tiles []tile.Tile) (c counts, ok bool) {
	for _, t := range tiles {
		if t.Kind >= tile.NumKinds {
			return counts{}, false
		}
		c[t.Kind]++
		if c[t.Kind] > tile.CopiesPerKind {
			return counts{}, false
		}
	}
	return c, true
}

// isSuitedIndex 报告下标是否为序数牌（0..26）；字牌不能组顺子。
func isSuitedIndex(index int) bool {
	return index >= 0 && index < suitedKindCount
}

// isTerminalOrHonor 报告下标是否为幺九牌或字牌（国士无双的 13 种牌）。
func isTerminalOrHonor(index int) bool {
	return tile.Kind(index).IsTerminalOrHonor()
}

// allZero 报告计数数组是否全为 0。
func allZero(c counts) bool {
	for _, n := range c {
		if n != 0 {
			return false
		}
	}
	return true
}

// firstNonZero 返回最小的非零下标；全为 0 时返回 -1。
func firstNonZero(c counts) int {
	for i := 0; i < tile.NumKinds; i++ {
		if c[i] != 0 {
			return i
		}
	}
	return -1
}
