// Package rules 实现日麻（立直麻将）的纯规则计算：和牌拆解（Decompose）与向听数（Shanten）。
//
// 依赖方向（契约 §1）：rules 只依赖 tile，不 import game / store / transport 等上层包，
// 因此 game 层可以放心调用本包做判胡与听牌判断。
//
// 所有导出函数都是纯函数：无 I/O、无全局可变状态、不改写入参，可并发调用。
// 错误一律用布尔返回值 / 哨兵数值表达（契约 §3 的签名没有 error），非法输入不会被静默当作「和牌」：
// Decompose 返回 ok=false，Shanten 返回 ShantenInvalid。
//
// 牌编码与 package tile 一致：34 种牌、下标 0..33（万筒索各 9 种 + 东南西北白发中），
// 每种 4 张；赤宝牌只影响 Tile.Aka 标记，不影响牌型判断（赤 5 与普通 5 同一 Kind）。
//
// 算法（面试可讲）：
//
//  1. 把 14 张手牌映射成 [34]uint8 计数数组，比 map 少了哈希开销、可整体值拷贝，
//     且「按牌种从小到大扫描」天然可行；
//  2. 标准型 = 4 个面子 + 1 个将牌。先枚举将牌（任一张 ≥ 2 张的牌），扣掉将牌后
//     从「最小非零牌」开始递归拆面子：它要么是刻子的首张，要么是顺子的首张，
//     只有这两个分支，因此回溯不重不漏；字牌下标 ≥ 27，永远进不了顺子分支；
//  3. 七对：所有牌种计数为偶数且总对数恰为 7（沿用 v1 语义：4 张相同算两对）；
//  4. 国士无双：13 种幺九字牌全部出现，且 14 张里必有一对；
//  5. 待ち类型由「和了牌落在拆解结果的哪一组」决定：顺子看位置（两面/嵌张/边张）、
//     刻子为双碰、将牌为单骑；多种拆解时按「两面 > 嵌张 > 边张 > 双碰 > 单骑」取一种。
package rules

import "summermahjong/riichi/internal/tile"

// Pattern 的取值（契约 §3）。
const (
	// PatternStandard 是标准型：4 面子 + 1 将。
	PatternStandard = "standard"
	// PatternSevenPairs 是七对子（沿用 v1 语义：4 张相同算两对）。
	PatternSevenPairs = "seven_pairs"
	// PatternKokushi 是国士无双。
	PatternKokushi = "kokushi"
)

// Meld 与 MeldShape 的 Kind 取值；字符串与契约 §2.3 的 game.Meld.Kind 完全一致。
const (
	// MeldChi 是顺子副露（吃）。
	MeldChi = "chi"
	// MeldPon 是刻子副露（碰）。
	MeldPon = "pon"
	// MeldKanDaiminkan 是大明杠（S5 副露支持时使用）。
	MeldKanDaiminkan = "kan_daiminkan"
	// MeldKanShouminkan 是加杠（S5 副露支持时使用）。
	MeldKanShouminkan = "kan_shouminkan"
	// MeldKanAnkan 是暗杠（S5 副露支持时使用）。
	MeldKanAnkan = "kan_ankan"
)

// WaitType 是待ち（和了牌与手牌形状的关系）；字符串即契约 §3 的取值。
type WaitType string

const (
	// WaitRyanmen 是两面待ち（如 45 等 3/6）。
	WaitRyanmen WaitType = "ryanmen"
	// WaitKanchan 是嵌张待ち（如 46 等 5）。
	WaitKanchan WaitType = "kanchan"
	// WaitPenchan 是边张待ち（如 12 等 3、89 等 7）。
	WaitPenchan WaitType = "penchan"
	// WaitShanpon 是双碰待ち（两对子等其中一张成刻）。
	WaitShanpon WaitType = "shanpon"
	// WaitTanki 是单骑待ち（单张等成将）。
	WaitTanki WaitType = "tanki"
)

// Meld 是 Decompose 入参里「已经副露的面子」。
//
// 与 package game 的 game.Meld 字段对齐（Kind/Tiles），但在这里独立声明：
// 契约 §1 要求 rules 不能 import game（避免依赖倒挂），game 层调用时逐字段转换即可。
//
// S3 第一版只支持门清（melds 传 nil / 空切片）；副露拆解在 S5 实现，
// 届时 Kind 取 MeldChi/MeldPon/MeldKan*，Tiles 为该副露包含的牌（杠为 4 张）。
type Meld struct {
	Kind  string
	Tiles []tile.Tile
}

// MeldShape 是拆解出来的一组面子（顺子或刻子）。
//
// 契约 §3 用 [3]tile.Tile 表示一组面子：顺子/刻子都是 3 张，
// 杠在计符/计役时按刻子处理（第 4 张不参与面子形状），故这里也固定 3 张。
type MeldShape struct {
	Kind  string
	Tiles [3]tile.Tile
}

// WinShape 是一次和牌拆解的完整结果（契约 §3）。
//
// 标准型：Melds 恰有 4 组、Jantou 为将牌；
// 七对/国士：Melds 为空，Jantou 为和了牌本身（没有传统意义上的将牌分组）。
type WinShape struct {
	Jantou  tile.Tile
	Melds   []MeldShape
	Wait    WaitType
	Pattern string
}

// IsWinning 报告手牌是否和牌；等价于 Decompose 的 ok。
//
// 参数约定与 Decompose 相同：hand 是完整的 14 张门清手牌（含和了牌），
// melds 目前必须为空（S3 只支持门清）。
func IsWinning(hand []tile.Tile, melds []Meld, winning tile.Tile) bool {
	_, ok := Decompose(hand, melds, winning)
	return ok
}

// Decompose 把手牌拆解成完整的和牌形状（标准型 / 七对 / 国士），并给出待ち类型。
//
// 参数约定：
//   - hand：完整的 14 张门清手牌，**必须包含和了牌**（13 张听牌形请由上层补上和了牌后调用，
//     例如 append(hand, winning)）；这样 13/14 张的语义只有一种，不会出现「少一张也算胡」。
//   - melds：已经副露的面子。S3 第一版只支持门清，melds 非空直接返回 false（S5 再实现）。
//   - winning：和了牌。其 Kind 必须出现在 hand 中（用于确定待ち类型；不要求是同一张物理牌）。
//
// 返回 ok=false 的典型情况：牌数不是 14、牌面非法（Kind 越界 / 同种超过 4 张）、
// 不和牌、winning 与 hand 不匹配、melds 非空。
//
// 牌型优先级与 v1 一致：标准型优先于七对（如 11223344556677m 返回 standard），国士不与前两者重叠。
func Decompose(hand []tile.Tile, melds []Meld, winning tile.Tile) (WinShape, bool) {
	// S3 只支持门清；副露后和牌的用例见 win_melds_test.go（S5 实现后启用）。
	if len(melds) != 0 {
		return WinShape{}, false
	}
	if len(hand) != winHandSize {
		return WinShape{}, false
	}

	c, ok := countTiles(hand)
	if !ok {
		return WinShape{}, false
	}
	// 和了牌必须是 14 张里出现过的牌种（否则说明调用方传错了牌）。
	if winning.Kind >= tile.NumKinds || c[winning.Kind] == 0 {
		return WinShape{}, false
	}

	if shape, ok := decomposeStandard(c, winning); ok {
		applyAka(&shape, hand, winning)
		return shape, true
	}
	if isSevenPairs(c) {
		shape := WinShape{Jantou: winning, Wait: WaitTanki, Pattern: PatternSevenPairs}
		applyAka(&shape, hand, winning)
		return shape, true
	}
	if isKokushi(c) {
		shape := WinShape{Jantou: winning, Wait: WaitTanki, Pattern: PatternKokushi}
		applyAka(&shape, hand, winning)
		return shape, true
	}
	return WinShape{}, false
}

// decomposeStandard 尝试把计数数组拆成「4 面子 + 1 将」；成功时返回待ち类型最优的那种拆解。
func decomposeStandard(c counts, winning tile.Tile) (WinShape, bool) {
	var (
		best    WinShape
		bestPri = int(^uint(0) >> 1) // 最大 int，等待被更小的优先级替换
		found   bool
	)

	for pair := 0; pair < tile.NumKinds; pair++ {
		if c[pair] < 2 {
			continue
		}
		rest := c
		rest[pair] -= 2
		jantou := tile.Tile{Kind: tile.Kind(pair)}
		var cur [4]MeldShape

		enumerateMeldShapes(rest, 0, &cur, func(melds [4]MeldShape) {
			wait, pri, ok := classifyWait(jantou, melds, winning.Kind)
			if !ok || pri >= bestPri {
				return
			}
			shape := WinShape{
				Jantou:  jantou,
				Melds:   make([]MeldShape, 4),
				Wait:    wait,
				Pattern: PatternStandard,
			}
			copy(shape.Melds, melds[:])
			best, bestPri, found = shape, pri, true
		})
	}
	if !found {
		return WinShape{}, false
	}
	return best, true
}

// enumerateMeldShapes 把剩余计数拆成 4 组面子；每找到一个完整拆解就回调 visit。
//
// 每次固定处理「剩余牌里下标最小的那张」：它只能作为刻子的首张或顺子的首张，
// 两个分支覆盖全部合法拆法。计数数组按值传递，分支之间互不影响。
func enumerateMeldShapes(rest counts, depth int, cur *[4]MeldShape, visit func([4]MeldShape)) {
	if depth == meldsRequired {
		if allZero(rest) {
			visit(*cur)
		}
		return
	}

	idx := firstNonZero(rest)
	if idx < 0 {
		return
	}

	// 分支一：刻子（三张相同）。
	if rest[idx] >= 3 {
		next := rest
		next[idx] -= 3
		kind := tile.Kind(idx)
		cur[depth] = MeldShape{Kind: MeldPon, Tiles: [3]tile.Tile{{Kind: kind}, {Kind: kind}, {Kind: kind}}}
		enumerateMeldShapes(next, depth+1, cur, visit)
	}

	// 分支二：顺子（同花色连续三张）。字牌（下标 ≥ 27）与跨花色都不允许。
	if isSuitedIndex(idx) && idx%9 <= 6 && rest[idx+1] > 0 && rest[idx+2] > 0 {
		next := rest
		next[idx]--
		next[idx+1]--
		next[idx+2]--
		cur[depth] = MeldShape{Kind: MeldChi, Tiles: [3]tile.Tile{
			{Kind: tile.Kind(idx)},
			{Kind: tile.Kind(idx + 1)},
			{Kind: tile.Kind(idx + 2)},
		}}
		enumerateMeldShapes(next, depth+1, cur, visit)
	}
}

// classifyWait 判断和了牌落在拆解结果的哪一组，得到待ち类型与优先级（数字越小越优先）。
//
// 同一手牌可能有多种拆解，导致待ち类型不唯一（例如 12345m 等 3m 既是两面也是边张）。
// 这里按「两面 > 嵌张 > 边张 > 双碰 > 单骑」取一种：两面待ち是平和判定的常见前提，
// 优先返回它对 S4 计分更友好；真正的最终取舍由 S4 按得点最大化决定。
func classifyWait(jantou tile.Tile, melds [4]MeldShape, winning tile.Kind) (WaitType, int, bool) {
	best := WaitType("")
	bestPri := int(^uint(0) >> 1)
	found := false
	consider := func(wait WaitType, pri int) {
		if pri < bestPri {
			best, bestPri, found = wait, pri, true
		}
	}

	for _, meld := range melds {
		for pos, t := range meld.Tiles {
			if t.Kind != winning {
				continue
			}
			if meld.Kind == MeldPon {
				consider(WaitShanpon, 3)
				continue
			}
			switch pos {
			case 0:
				// 和了牌是顺子首张：若顺子是 789，则原来是 89 等 7 —— 边张；否则是两面。
				if meld.Tiles[2].Kind.Number() == 9 {
					consider(WaitPenchan, 2)
				} else {
					consider(WaitRyanmen, 0)
				}
			case 1:
				// 和了牌在顺子中间：嵌张。
				consider(WaitKanchan, 1)
			default:
				// 和了牌是顺子末张：若顺子是 123，则原来是 12 等 3 —— 边张；否则是两面。
				if meld.Tiles[0].Kind.Number() == 1 {
					consider(WaitPenchan, 2)
				} else {
					consider(WaitRyanmen, 0)
				}
			}
		}
	}

	// 和了牌在将牌上：单骑待ち。（和了牌也可能同时出现在面子与将牌里，例如 11123m，
	// 此时上面的面子分支优先级更高，会得到两面/双碰等更具体的待ち。）
	if jantou.Kind == winning {
		consider(WaitTanki, 4)
	}

	return best, bestPri, found
}

// applyAka 把入参里的赤宝牌标记补回拆解结果。
//
// 拆解只用 Kind 计算，重建 MeldShape 时会丢掉 Tile.Aka；这里按「每种牌至多一张赤牌」
// 的规则，把原手牌（以及和了牌）中的赤 5 标记落到形状里第一张对应牌上，
// 供 S4 结算赤宝牌、S5 回放时正确显示。
func applyAka(shape *WinShape, hand []tile.Tile, winning tile.Tile) {
	red := make(map[tile.Kind]bool, 3)
	for _, t := range hand {
		if t.Aka {
			red[t.Kind] = true
		}
	}
	if winning.Aka {
		red[winning.Kind] = true
	}
	mark := func(t *tile.Tile) {
		if red[t.Kind] {
			t.Aka = true
			red[t.Kind] = false
		}
	}

	mark(&shape.Jantou)
	for i := range shape.Melds {
		for j := range shape.Melds[i].Tiles {
			mark(&shape.Melds[i].Tiles[j])
		}
	}
}

// isSevenPairs 判断计数数组是否满足七对子。
//
// 沿用 v1 语义：4 张相同算两对（日麻严格规则里「四张相同」只算一对，这里以 v1 对拍为准）。
// 14 张牌下「所有计数为偶数且总对数为 7」即七对。
func isSevenPairs(c counts) bool {
	pairs := 0
	for _, n := range c {
		if n%2 != 0 {
			return false
		}
		pairs += int(n / 2)
	}
	return pairs == sevenPairsCount
}

// isKokushi 判断计数数组是否满足国士无双（13 种幺九字牌全部出现且成对）。
//
// 14 张牌里 13 种幺九字牌全在，必然且只能有一对；出现任何中张都直接判否。
func isKokushi(c counts) bool {
	distinct := 0
	for i := 0; i < tile.NumKinds; i++ {
		if !isTerminalOrHonor(i) {
			if c[i] != 0 {
				return false
			}
			continue
		}
		if c[i] == 0 {
			return false
		}
		distinct++
	}
	return distinct == kokushiKinds
}
