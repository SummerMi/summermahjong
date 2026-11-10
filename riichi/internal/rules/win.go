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

import (
	"slices"
	"sort"

	"summermahjong/riichi/internal/tile"
)

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
// Kind 取 MeldChi / MeldPon / MeldKanDaiminkan / MeldKanShouminkan / MeldKanAnkan；
// Tiles 为该副露包含的牌：吃/碰 3 张，杠 4 张。明暗由 Kind 决定（暗杠是唯一暗面副露）。
type Meld struct {
	Kind  string
	Tiles []tile.Tile
}

// MeldShape 是拆解出来的一组面子（顺子 / 刻子 / 杠）。
//
// 契约 v1.2 起 Tiles 是变长切片：
//   - 吃/碰为 3 张；
//   - 杠为 4 张（fu/yaku 里按 len(Tiles)==4 识别杠，按 Kind 区分吃/碰/杠）。
//
// Open 表示这组面子是否是「副露（明面）」：
//   - S3 的门清拆解恒为 false（暗刻/暗杠）；
//   - S5 转换 game.Meld 时，吃/碰/大明杠 = true，暗杠 = false；
//   - 符计算（明刻 2 / 暗刻 4、明杠 8 / 暗杠 16）与三暗刻/四暗刻判定都依赖它。
type MeldShape struct {
	Kind  string
	Tiles []tile.Tile
	Open  bool
}

// WinShape 是一次和牌拆解的完整结果（契约 §3，v1.2 新增 Tiles 字段）。
//
// 标准型：Melds 恰有 4 组、Jantou 为将牌；
// 七对/国士：Melds 为空，Jantou 为和了牌本身（没有传统意义上的将牌分组）。
//
// Tiles 是构成这手和牌的全部物理牌（14 张；含杠的第 4 张，因此带杠时是 15 张）。
// 计分需要它来统计宝牌、判断混一色/清一色/断幺九、以及七对形的花色役；
// 仅靠 Melds+Jantou 无法还原七对/国士的全部牌。S5 手工构造 shape 时也应填上。
// 若留空，依赖面子的判定仍可用，但七对/国士的花色役与宝牌统计会缺失。
type WinShape struct {
	Jantou  tile.Tile
	Melds   []MeldShape
	Wait    WaitType
	Pattern string
	Tiles   []tile.Tile
}

// IsWinning 报告手牌是否和牌；等价于 Decompose 的 ok。
//
// 参数约定与 Decompose 相同：hand 只含暗牌（含和了牌），melds 是已经副露的面子。
func IsWinning(hand []tile.Tile, melds []Meld, winning tile.Tile) bool {
	return IsWinningWithOptions(hand, melds, winning, Options{})
}

// IsWinningWithOptions 是带规则选项的 IsWinning，语义开关见 Options。
func IsWinningWithOptions(hand []tile.Tile, melds []Meld, winning tile.Tile, opts Options) bool {
	_, ok := DecomposeWithOptions(hand, melds, winning, opts)
	return ok
}

// Decompose 把手牌拆解成完整的和牌形状（标准型 / 七对 / 国士），并给出待ち类型。
//
// 使用默认规则（日麻严格七对语义）；需要 v1（中国规则）语义时用 DecomposeWithOptions。
//
// 参数约定（S5b 起支持副露）：
//   - hand：**暗牌部分**，必须包含和了牌（和了牌由上层补进暗牌）；张数 = 14 - 3*len(melds)。
//     例：无副露 14 张；1 组副露 11 张；4 组副露时只剩将牌 2 张。
//   - melds：已经副露的面子，0-4 组。吃/碰 3 张，杠（含大明杠/加杠/暗杠）4 张。
//     返回的 WinShape.Melds 会保留 Kind / Tiles / Open：吃、碰、明杠 Open=true，暗杠 Open=false；
//     暗牌里拆出来的面子 Open=false。
//   - winning：和了牌。其 Kind 必须出现在 hand 中（用于确定待ち类型）。
//
// 返回 ok=false 的典型情况：暗牌张数不对、副露结构非法（类型/张数/牌面不合法）、
// 牌面非法（Kind 越界 / 同种超过 4 张）、不和牌、winning 与暗牌不匹配。
//
// 牌型优先级与 v1 一致：标准型优先于七对（如 11223344556677m 返回 standard）；
// 有副露时七对/国士不可能成立，只做标准型拆解。
func Decompose(hand []tile.Tile, melds []Meld, winning tile.Tile) (WinShape, bool) {
	return DecomposeWithOptions(hand, melds, winning, Options{})
}

// DecomposeWithOptions 是带规则选项的 Decompose。
//
// 唯一的语义开关是七对子口径（见 Options）：
// 默认日麻严格语义「7 种不同的对子」；SevenPairsStrict=false 时按 v1 中国规则
// 「4 张相同算两对」，用于 v1 金标对拍。
func DecomposeWithOptions(hand []tile.Tile, melds []Meld, winning tile.Tile, opts Options) (WinShape, bool) {
	if len(melds) > meldsRequired {
		return WinShape{}, false
	}
	called, ok := buildCalledMelds(melds)
	if !ok {
		return WinShape{}, false
	}
	// 暗牌张数：14 张和了形里，每组副露占掉 3 张（杠多出的那张在副露内部）。
	if len(hand) != winHandSize-3*len(melds) {
		return WinShape{}, false
	}

	c, ok := countTiles(hand)
	if !ok {
		return WinShape{}, false
	}
	// 和了牌必须是暗牌里出现过的牌种（副露的牌不可能再被和了）。
	if winning.Kind >= tile.NumKinds || c[winning.Kind] == 0 {
		return WinShape{}, false
	}
	if !withinDeck(hand, called) {
		return WinShape{}, false
	}

	required := meldsRequired - len(called)
	if required == meldsRequired {
		// 门清：标准型 → 七对 → 国士（与 v1 一致的标准型优先）。
		if jantou, closedMelds, wait, ok := decomposeClosedStandard(c, required, winning.Kind); ok {
			shape := buildStandardShape(hand, called, jantou, closedMelds, wait, winning)
			return shape, true
		}
		if isSevenPairs(c, !opts.StrictSevenPairs()) {
			shape := WinShape{
				Jantou:  winning,
				Wait:    WaitTanki,
				Pattern: PatternSevenPairs,
				Tiles:   slices.Clone(hand),
			}
			applyAka(&shape, hand, winning)
			return shape, true
		}
		if isKokushi(c) {
			shape := WinShape{
				Jantou:  winning,
				Wait:    WaitTanki,
				Pattern: PatternKokushi,
				Tiles:   slices.Clone(hand),
			}
			applyAka(&shape, hand, winning)
			return shape, true
		}
		return WinShape{}, false
	}

	// 有副露时只可能是标准型：暗牌里拆出 4-len(melds) 组面子 + 1 个将牌。
	jantou, closedMelds, wait, ok := decomposeClosedStandard(c, required, winning.Kind)
	if !ok {
		return WinShape{}, false
	}
	return buildStandardShape(hand, called, jantou, closedMelds, wait, winning), true
}

// decomposeClosedStandard 从暗牌计数里拆出「required 组面子 + 1 个将牌」。
//
// 返回待ち类型最优的那种拆解（优先级见 classifyWait）。
func decomposeClosedStandard(c counts, required int, winning tile.Kind) (tile.Tile, []MeldShape, WaitType, bool) {
	var (
		bestJantou tile.Tile
		bestMelds  []MeldShape
		bestWait   WaitType
		bestPri    = int(^uint(0) >> 1) // 最大 int，等待被更小的优先级替换
		found      bool
	)

	for pair := 0; pair < tile.NumKinds; pair++ {
		if c[pair] < 2 {
			continue
		}
		rest := c
		rest[pair] -= 2
		jantou := tile.Tile{Kind: tile.Kind(pair)}
		cur := make([]MeldShape, required)

		enumerateMeldShapes(rest, 0, required, cur, func(melds []MeldShape) {
			wait, pri, ok := classifyWait(jantou, melds, winning)
			if !ok || pri >= bestPri {
				return
			}
			bestJantou = jantou
			bestMelds = append([]MeldShape(nil), melds...)
			bestWait = wait
			bestPri = pri
			found = true
		})
	}
	return bestJantou, bestMelds, bestWait, found
}

// buildStandardShape 组装标准型的 WinShape：
// 先用 applyAka 只处理暗牌部分（副露牌的赤宝牌标记由调用方直接带进来），再合并副露、排序、记录全部牌。
func buildStandardShape(hand []tile.Tile, called []MeldShape, jantou tile.Tile, closed []MeldShape, wait WaitType, winning tile.Tile) WinShape {
	shape := WinShape{Jantou: jantou, Melds: closed, Wait: wait, Pattern: PatternStandard}
	applyAka(&shape, hand, winning)

	merged := make([]MeldShape, 0, len(called)+len(closed))
	merged = append(merged, called...)
	merged = append(merged, shape.Melds...)
	sortMelds(merged)
	shape.Melds = merged
	shape.Tiles = handAndMeldTiles(hand, called)
	return shape
}

// buildCalledMelds 校验并转换 Decompose 的 melds 入参。
//
// 规则（契约 §2.3 的副露种类）：
//   - chi：3 张同花色连续牌，明面（Open=true）；
//   - pon：3 张同种牌，明面；
//   - kan_daiminkan / kan_shouminkan：4 张同种牌，明面；
//   - kan_ankan：4 张同种牌，暗面（Open=false，不破坏门清）。
//
// 任一结构非法即返回 ok=false。
func buildCalledMelds(melds []Meld) ([]MeldShape, bool) {
	out := make([]MeldShape, 0, len(melds))
	for _, meld := range melds {
		for _, t := range meld.Tiles {
			if t.Kind >= tile.NumKinds {
				return nil, false
			}
		}
		tiles := slices.Clone(meld.Tiles)
		switch meld.Kind {
		case MeldChi:
			if len(tiles) != 3 || !isChiTiles(tiles) {
				return nil, false
			}
			out = append(out, MeldShape{Kind: MeldChi, Tiles: tiles, Open: true})
		case MeldPon:
			if len(tiles) != 3 || !isPonTiles(tiles) {
				return nil, false
			}
			out = append(out, MeldShape{Kind: MeldPon, Tiles: tiles, Open: true})
		case MeldKanDaiminkan, MeldKanShouminkan:
			if len(tiles) != 4 || !isPonTiles(tiles) {
				return nil, false
			}
			out = append(out, MeldShape{Kind: meld.Kind, Tiles: tiles, Open: true})
		case MeldKanAnkan:
			if len(tiles) != 4 || !isPonTiles(tiles) {
				return nil, false
			}
			out = append(out, MeldShape{Kind: MeldKanAnkan, Tiles: tiles, Open: false})
		default:
			return nil, false
		}
	}
	return out, true
}

// sortMelds 把面子按首张牌升序排列，保证拆解结果稳定可复现。
func sortMelds(melds []MeldShape) {
	sort.SliceStable(melds, func(i, j int) bool {
		return melds[i].Tiles[0].Kind < melds[j].Tiles[0].Kind
	})
}

// handAndMeldTiles 返回「暗牌 + 副露牌」的完整物理牌列表（含杠的第 4 张）。
func handAndMeldTiles(hand []tile.Tile, called []MeldShape) []tile.Tile {
	out := slices.Clone(hand)
	for _, meld := range called {
		out = append(out, meld.Tiles...)
	}
	return out
}

// withinDeck 校验「暗牌 + 副露牌」合在一起每种牌不超过 4 张。
func withinDeck(hand []tile.Tile, called []MeldShape) bool {
	var c counts
	for _, t := range hand {
		if t.Kind >= tile.NumKinds {
			return false
		}
		c[t.Kind]++
	}
	for _, meld := range called {
		for _, t := range meld.Tiles {
			c[t.Kind]++
		}
	}
	for _, n := range c {
		if n > tile.CopiesPerKind {
			return false
		}
	}
	return true
}

// enumerateMeldShapes 把剩余计数拆成 required 组面子；每找到一个完整拆解就回调 visit。
//
// 每次固定处理「剩余牌里下标最小的那张」：它只能作为刻子的首张或顺子的首张，
// 两个分支覆盖全部合法拆法。计数数组按值传递，分支之间互不影响。
func enumerateMeldShapes(rest counts, depth, required int, cur []MeldShape, visit func([]MeldShape)) {
	if depth == required {
		if allZero(rest) {
			visit(cur)
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
		cur[depth] = MeldShape{Kind: MeldPon, Tiles: []tile.Tile{{Kind: kind}, {Kind: kind}, {Kind: kind}}}
		enumerateMeldShapes(next, depth+1, required, cur, visit)
	}

	// 分支二：顺子（同花色连续三张）。字牌（下标 ≥ 27）与跨花色都不允许。
	if isSuitedIndex(idx) && idx%9 <= 6 && rest[idx+1] > 0 && rest[idx+2] > 0 {
		next := rest
		next[idx]--
		next[idx+1]--
		next[idx+2]--
		cur[depth] = MeldShape{Kind: MeldChi, Tiles: []tile.Tile{
			{Kind: tile.Kind(idx)},
			{Kind: tile.Kind(idx + 1)},
			{Kind: tile.Kind(idx + 2)},
		}}
		enumerateMeldShapes(next, depth+1, required, cur, visit)
	}
}

// classifyWait 判断和了牌落在**暗牌拆解结果**的哪一组，得到待ち类型与优先级（数字越小越优先）。
//
// 只传入暗牌里拆出来的面子：副露是更早完成的动作，和了牌不可能落在其中。
// 同一手牌可能有多种拆解，导致待ち类型不唯一（例如 12345m 等 3m 既是两面也是边张）。
// 这里按「两面 > 嵌张 > 边张 > 双碰 > 单骑」取一种：两面待ち是平和判定的常见前提，
// 优先返回它对 S4 计分更友好；真正的最终取舍由 S4 按得点最大化决定。
func classifyWait(jantou tile.Tile, melds []MeldShape, winning tile.Kind) (WaitType, int, bool) {
	best := WaitType("")
	bestPri := int(^uint(0) >> 1)
	found := false
	consider := func(wait WaitType, pri int) {
		if pri < bestPri {
			best, bestPri, found = wait, pri, true
		}
	}

	for _, meld := range melds {
		pos := -1
		for i, t := range meld.Tiles {
			if t.Kind == winning {
				pos = i
				break
			}
		}
		if pos < 0 {
			continue
		}
		if meld.Kind != MeldChi {
			// 刻子/杠：和了牌补成刻子 → 双碰。（抢杠和了时也走这里。）
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
// compat=false（日麻严格）：必须是 7 种不同的对子，每种恰好 2 张；
// 4 张相同不能算两对（那 2 张多余的牌凑不出第 7 种对子）。
//
// compat=true（v1 / 中国规则）：沿用 v1 语义，4 张相同算两对，
// 即「所有计数为偶数且总对数为 7」。
func isSevenPairs(c counts, compat bool) bool {
	if compat {
		pairs := 0
		for _, n := range c {
			if n%2 != 0 {
				return false
			}
			pairs += int(n / 2)
		}
		return pairs == sevenPairsCount
	}

	kinds := 0
	for _, n := range c {
		switch n {
		case 0:
			continue
		case 2:
			kinds++
		default:
			return false
		}
	}
	return kinds == sevenPairsCount
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
