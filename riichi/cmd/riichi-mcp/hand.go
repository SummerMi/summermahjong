package main

import (
	"sort"
	"strconv"

	"summermahjong/riichi/internal/tile"
)

// parseHandArg 解析工具参数里的牌面数组（如 ["1m","2m","r5p"]），至少一张。
//
// 所有牌面统一走 tile.ParseTile：支持 "1m".."9s"、"E/S/W/N/P/F/C" 以及
// 赤宝牌写法 "r5m"/"0m"；非法牌面返回 -32602 参数错误。
func parseHandArg(names []string, field string) ([]tile.Tile, error) {
	if len(names) == 0 {
		return nil, invalidParams("%s 不能为空", field)
	}
	return parseTiles(names, field)
}

// parseTilesAllowEmpty 与 parseHandArg 相同，但允许空数组（舍牌 / 宝牌指示牌可以为空）。
func parseTilesAllowEmpty(names []string, field string) ([]tile.Tile, error) {
	return parseTiles(names, field)
}

// parseTiles 是 parseHandArg / parseTilesAllowEmpty 的共同实现。
func parseTiles(names []string, field string) ([]tile.Tile, error) {
	out := make([]tile.Tile, 0, len(names))
	for i, name := range names {
		t, err := tile.ParseTile(name)
		if err != nil {
			return nil, invalidParams("%s[%d]=%q 不是合法牌面（示例：1m / 9s / E / r5p）", field, i, name)
		}
		out = append(out, t)
	}
	if err := validateTileCounts(out); err != nil {
		return nil, err
	}
	return out, nil
}

// validateTileCounts 校验「同种牌不超过 4 张」（和牌/手牌的基本合法性）。
func validateTileCounts(tiles []tile.Tile) error {
	var counts [tile.NumKinds]int
	for _, t := range tiles {
		if t.Kind >= tile.NumKinds {
			return invalidParams("未知牌面: %s", t.String())
		}
		counts[t.Kind]++
		if counts[t.Kind] > tile.CopiesPerKind {
			return invalidParams("同种牌超过 %d 张: %s", tile.CopiesPerKind, t.Kind)
		}
	}
	return nil
}

// tilesToStrings 把牌切片转成 MCP 输出用的文本数组。
func tilesToStrings(tiles []tile.Tile) []string {
	out := make([]string, len(tiles))
	for i, t := range tiles {
		out[i] = t.String()
	}
	return out
}

// tileCounts 统计 34 种牌的数量。
func tileCounts(tiles []tile.Tile) [tile.NumKinds]uint8 {
	var counts [tile.NumKinds]uint8
	for _, t := range tiles {
		if t.Kind < tile.NumKinds {
			counts[t.Kind]++
		}
	}
	return counts
}

// removeTileAt 返回去掉第 i 张后的新切片（不修改入参）。
func removeTileAt(tiles []tile.Tile, i int) []tile.Tile {
	out := make([]tile.Tile, 0, len(tiles)-1)
	out = append(out, tiles[:i]...)
	out = append(out, tiles[i+1:]...)
	return out
}

// sortedKinds 返回牌里出现的不同牌种，按 34 种牌的自然顺序排列。
func sortedKinds(tiles []tile.Tile) []tile.Kind {
	seen := make(map[tile.Kind]struct{}, len(tiles))
	kinds := make([]tile.Kind, 0, len(tiles))
	for _, t := range tiles {
		if t.Kind >= tile.NumKinds {
			continue
		}
		if _, ok := seen[t.Kind]; ok {
			continue
		}
		seen[t.Kind] = struct{}{}
		kinds = append(kinds, t.Kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// containsKind 报告一组牌里是否出现过某牌种（只看 Kind，不看赤宝牌）。
func containsKind(tiles []tile.Tile, kind tile.Kind) bool {
	for _, t := range tiles {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

// suitedKindAt 返回同花色、指定点数的牌种；越界 / 字牌返回 false。
func suitedKindAt(kind tile.Kind, number int) (tile.Kind, bool) {
	if kind.IsHonor() || number < 1 || number > 9 {
		return 0, false
	}
	base := int(kind) - (kind.Number() - 1)
	return tile.Kind(base + number - 1), true
}

// remainingCopies 返回某牌种在「未可见区域」还剩几张（0..4）。
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

// dangerForKind 估计「打出某牌种」对一个已立直对手的放铳危险度（0..100，越大越危险）。
//
// 与 internal/agent 的 DefenseAgent 同一套启发式（agent 里的函数未导出，工具层独立实现）：
//   - 现物：对手舍牌里已有同牌种 → 0（他不能荣和现物）；
//   - 筋：同花色 ±3 已打 → ×0.35（两面良形被排除）；
//   - 壁：同花色 ±2 已 4 张全见 → ×0.55（两面搭子不存在）；
//   - 剩余张数：越少越安全（×0.55..×1.15）；
//   - 中张比幺九/字牌更危险。
func dangerForKind(discards []tile.Tile, kind tile.Kind, visible [tile.NumKinds]uint8) (float64, []string) {
	if kind >= tile.NumKinds {
		return 0, nil
	}
	reasons := make([]string, 0, 4)
	if containsKind(discards, kind) {
		return 0, append(reasons, "现物（对手舍牌里已有同牌种）")
	}

	remaining := remainingCopies(visible, kind)
	base := baseDanger(kind, remaining)
	switch {
	case kind.IsHonor():
		reasons = append(reasons, "字牌")
	case kind.IsTerminal():
		reasons = append(reasons, "幺九")
	default:
		reasons = append(reasons, "中张")
	}

	if !kind.IsHonor() {
		if discardedOffset(discards, kind, -3) || discardedOffset(discards, kind, 3) {
			base *= 0.35
			reasons = append(reasons, "筋（±3 已打，两面被排除）")
		}
		if wallOn(kind, -2, visible) || wallOn(kind, 2, visible) {
			base *= 0.55
			reasons = append(reasons, "壁（±2 已见 4 张）")
		}
	}

	factor := 0.55 + 0.15*float64(remaining)
	if remaining == 0 {
		factor = 0.3
	}
	reasons = append(reasons, "剩余 "+strconv.Itoa(remaining)+" 张")
	return base * factor, reasons
}

// baseDanger 是按牌型给的基准危险度（不看筋/壁/剩余张数）。
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

// discardedOffset 报告对手舍牌里是否有「同花色、点数为 n+delta」的牌。
func discardedOffset(discards []tile.Tile, kind tile.Kind, delta int) bool {
	target, ok := suitedKindAt(kind, kind.Number()+delta)
	if !ok {
		return false
	}
	return containsKind(discards, target)
}

// wallOn 报告「同花色、点数为 n+delta」的牌是否 4 张全部可见（形成壁）。
func wallOn(kind tile.Kind, delta int, visible [tile.NumKinds]uint8) bool {
	target, ok := suitedKindAt(kind, kind.Number()+delta)
	if !ok {
		return false
	}
	return int(visible[target]) >= tile.CopiesPerKind
}
