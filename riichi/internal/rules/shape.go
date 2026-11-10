package rules

import (
	"errors"
	"fmt"

	"summermahjong/riichi/internal/tile"
)

// 计分相关的哨兵错误（风格与契约 §7 一致：上层用 errors.Is 判断类别）。
var (
	// ErrInvalidShape 表示传入的 WinShape 结构不自洽（牌型未知、面子数量/牌面不合法等）。
	ErrInvalidShape = errors.New("rules: 无效的和牌形状")
	// ErrInvalidContext 表示 ScoreContext 与和了方式矛盾（例如一发但未立直、海底却荣和）。
	ErrInvalidContext = errors.New("rules: 无效的计分上下文")
)

// ShapeTiles 返回构成这手和牌的全部物理牌。
//
// 优先返回拆解时记录的 shape.Tiles（Decompose 会填好，七对/国士也有全部 14 张）；
// 若为空（手工构造的 shape），则由「将牌 × 2 + 各面子」拼出（杠按 4 张）。
func ShapeTiles(shape WinShape) []tile.Tile {
	if len(shape.Tiles) > 0 {
		return shape.Tiles
	}
	tiles := make([]tile.Tile, 0, winHandSize)
	tiles = append(tiles, shape.Jantou, shape.Jantou)
	for _, meld := range shape.Melds {
		tiles = append(tiles, meld.Tiles...)
	}
	return tiles
}

// shapeCounts 统计 shape 全部牌的张数（下标即 tile.Kind）。
func shapeCounts(shape WinShape) counts {
	var c counts
	for _, t := range ShapeTiles(shape) {
		if t.Kind < tile.NumKinds {
			c[t.Kind]++
		}
	}
	return c
}

// shapeMenzen 报告 shape 是否门清：没有任何 Open 的副露即门清（暗杠不算副露）。
func shapeMenzen(shape WinShape) bool {
	for _, meld := range shape.Melds {
		if meld.Open {
			return false
		}
	}
	return true
}

// chiMelds 返回所有顺子面子（吃）。
func chiMelds(shape WinShape) []MeldShape {
	out := make([]MeldShape, 0, len(shape.Melds))
	for _, meld := range shape.Melds {
		if meld.Kind == MeldChi {
			out = append(out, meld)
		}
	}
	return out
}

// ponLikeMelds 返回所有刻子/杠面子（碰、明杠、暗杠）。
func ponLikeMelds(shape WinShape) []MeldShape {
	out := make([]MeldShape, 0, len(shape.Melds))
	for _, meld := range shape.Melds {
		if meld.Kind != MeldChi {
			out = append(out, meld)
		}
	}
	return out
}

// kanCount 返回杠的数量（按 len(Tiles)==4 识别，契约 v1.2）。
func kanCount(shape WinShape) int {
	n := 0
	for _, meld := range shape.Melds {
		if len(meld.Tiles) == 4 {
			n++
		}
	}
	return n
}

// concealedPonCount 返回暗刻 + 暗杠的数量（三暗刻/四暗刻用）。
func concealedPonCount(shape WinShape) int {
	n := 0
	for _, meld := range shape.Melds {
		if meld.Kind != MeldChi && !meld.Open && len(meld.Tiles) >= 3 {
			n++
		}
	}
	return n
}

// dragonTripletCount 返回三元牌（白发中）刻子的数量。
func dragonTripletCount(shape WinShape) int {
	n := 0
	for _, meld := range ponLikeMelds(shape) {
		if meld.Tiles[0].Kind.IsDragon() {
			n++
		}
	}
	return n
}

// windTripletCount 返回风牌（东南西北）刻子的数量。
func windTripletCount(shape WinShape) int {
	n := 0
	for _, meld := range ponLikeMelds(shape) {
		if meld.Tiles[0].Kind.IsWind() {
			n++
		}
	}
	return n
}

// isValuePair 报告雀头是否为役牌（三元牌 / 自风 / 场风）。
func isValuePair(kind tile.Kind, ctx ScoreContext) bool {
	return kind.IsDragon() || kind == ctx.SeatWind || kind == ctx.RoundWind
}

// valuePairFu 返回雀头加符：三元牌/自风/场风各 +2；连风（自风=场风）合计 +4。
func valuePairFu(kind tile.Kind, ctx ScoreContext) int {
	fu := 0
	if kind.IsDragon() {
		fu += 2
	}
	if kind == ctx.SeatWind {
		fu += 2
	}
	if kind == ctx.RoundWind {
		fu += 2
	}
	return fu
}

// allPonShape 报告标准型是否全部由刻子/杠组成（对对和）。
func allPonShape(shape WinShape) bool {
	if shape.Pattern != PatternStandard {
		return false
	}
	for _, meld := range shape.Melds {
		if meld.Kind == MeldChi {
			return false
		}
	}
	return true
}

// pinfuShape 判断是否具备平和役成立条件：门清 + 四组顺子 + 非役牌雀头 + 两面待ち。
//
// 「形」的部分由 pinfuShapeAny 判断，符计算（平和自摸 20 符）也复用它。
func pinfuShape(shape WinShape, ctx ScoreContext) bool {
	return shapeMenzen(shape) && pinfuShapeAny(shape, ctx)
}

// isChiTiles 判断三张牌是否构成合法顺子（同花色、连续、非字牌）。
func isChiTiles(tiles []tile.Tile) bool {
	if len(tiles) != 3 {
		return false
	}
	a, b, c := tiles[0].Kind, tiles[1].Kind, tiles[2].Kind
	if a.Suit() == tile.Honor || a.Suit() != b.Suit() || b.Suit() != c.Suit() {
		return false
	}
	return b == a+1 && c == b+1
}

// isPonTiles 判断牌组是否全部同种（刻子 3 张 / 杠 4 张）。
func isPonTiles(tiles []tile.Tile) bool {
	if len(tiles) != 3 && len(tiles) != 4 {
		return false
	}
	for _, t := range tiles[1:] {
		if t.Kind != tiles[0].Kind {
			return false
		}
	}
	return true
}

// allSimple 报告是否全中张（不含幺九牌与字牌）——断幺九。
func allSimple(c counts) bool {
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] > 0 && tile.Kind(kind).IsTerminalOrHonor() {
			return false
		}
	}
	return true
}

// allHonors 报告是否全字牌——字一色。
func allHonors(c counts) bool {
	has := false
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] == 0 {
			continue
		}
		if !tile.Kind(kind).IsHonor() {
			return false
		}
		has = true
	}
	return has
}

// allTerminals 报告是否全幺九数牌（不含字牌）——清老头。
func allTerminals(c counts) bool {
	has := false
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] == 0 {
			continue
		}
		if !tile.Kind(kind).IsTerminal() {
			return false
		}
		has = true
	}
	return has
}

// allTerminalOrHonor 报告是否全幺九牌或字牌——混老头/混全带幺九的牌面条件。
func allTerminalOrHonor(c counts) bool {
	has := false
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] == 0 {
			continue
		}
		if !tile.Kind(kind).IsTerminalOrHonor() {
			return false
		}
		has = true
	}
	return has
}

// allGreen 报告是否全是绿一色允许的牌：2/3/4/6/8 索 + 发。
func allGreen(c counts) bool {
	green := map[tile.Kind]bool{
		tile.Sou2: true, tile.Sou3: true, tile.Sou4: true,
		tile.Sou6: true, tile.Sou8: true, tile.Hatsu: true,
	}
	has := false
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] == 0 {
			continue
		}
		if !green[tile.Kind(kind)] {
			return false
		}
		has = true
	}
	return has
}

// suitSet 返回牌面涉及的花色集合与是否含字牌。
func suitSet(c counts) (suits map[tile.Suit]bool, hasHonor bool) {
	suits = make(map[tile.Suit]bool, 3)
	for kind := 0; kind < tile.NumKinds; kind++ {
		if c[kind] == 0 {
			continue
		}
		k := tile.Kind(kind)
		if k.IsHonor() {
			hasHonor = true
			continue
		}
		suits[k.Suit()] = true
	}
	return suits, hasHonor
}

// isChinitsu 报告是否清一色：只有一种数牌花色、没有字牌。
func isChinitsu(c counts) bool {
	suits, hasHonor := suitSet(c)
	return !hasHonor && len(suits) == 1
}

// isHonitsu 报告是否混一色：只有一种数牌花色 + 至少一张字牌。
func isHonitsu(c counts) bool {
	suits, hasHonor := suitSet(c)
	return hasHonor && len(suits) == 1
}

// chiPairCount 返回「相同顺子」的对数：一组一盃口算 1，两组算 2。
func chiPairCount(shape WinShape) int {
	seen := make(map[tile.Kind]int, 4)
	for _, meld := range chiMelds(shape) {
		seen[meld.Tiles[0].Kind]++
	}
	pairs := 0
	for _, n := range seen {
		pairs += n / 2
	}
	return pairs
}

// sanshokuDoujun 报告是否三色同顺：万/筒/索各有一组相同数字的顺子。
func sanshokuDoujun(shape WinShape) bool {
	// key: 顺子起始点数（1..7），value: 已出现的花色集合
	seen := make(map[int]map[tile.Suit]bool, 7)
	for _, meld := range chiMelds(shape) {
		num := meld.Tiles[0].Kind.Number()
		if seen[num] == nil {
			seen[num] = make(map[tile.Suit]bool, 3)
		}
		seen[num][meld.Tiles[0].Kind.Suit()] = true
	}
	for _, suits := range seen {
		if len(suits) == 3 {
			return true
		}
	}
	return false
}

// ittsu 报告是否一气通贯：同一花色同时有 123、456、789。
func ittsu(shape WinShape) bool {
	// key: 花色，value: 起始点数集合
	seen := make(map[tile.Suit]map[int]bool, 3)
	for _, meld := range chiMelds(shape) {
		suit := meld.Tiles[0].Kind.Suit()
		if seen[suit] == nil {
			seen[suit] = make(map[int]bool, 3)
		}
		seen[suit][meld.Tiles[0].Kind.Number()] = true
	}
	for _, nums := range seen {
		if nums[1] && nums[4] && nums[7] {
			return true
		}
	}
	return false
}

// sanshokuDoukou 报告是否三色同刻：万/筒/索各有一组相同数字的刻子。
func sanshokuDoukou(shape WinShape) bool {
	seen := make(map[int]map[tile.Suit]bool, 9)
	for _, meld := range ponLikeMelds(shape) {
		kind := meld.Tiles[0].Kind
		if kind.IsHonor() {
			continue
		}
		if seen[kind.Number()] == nil {
			seen[kind.Number()] = make(map[tile.Suit]bool, 3)
		}
		seen[kind.Number()][kind.Suit()] = true
	}
	for _, suits := range seen {
		if len(suits) == 3 {
			return true
		}
	}
	return false
}

// chanta 报告是否具备混全带幺九形：所有面子与雀头都含幺九牌/字牌、至少一组顺子、且含字牌。
func chanta(shape WinShape, c counts) bool {
	if shape.Pattern != PatternStandard || len(chiMelds(shape)) == 0 {
		return false
	}
	if !shape.Jantou.Kind.IsTerminalOrHonor() {
		return false
	}
	for _, meld := range shape.Melds {
		if !meldContainsTerminalOrHonor(meld) {
			return false
		}
	}
	_, hasHonor := suitSet(c)
	return hasHonor
}

// junchan 报告是否具备纯全带幺九形：所有面子与雀头都含幺九牌（无字牌）、至少一组顺子。
func junchan(shape WinShape, c counts) bool {
	if shape.Pattern != PatternStandard || len(chiMelds(shape)) == 0 {
		return false
	}
	if !shape.Jantou.Kind.IsTerminal() {
		return false
	}
	for _, meld := range shape.Melds {
		if !meldContainsTerminal(meld) {
			return false
		}
	}
	_, hasHonor := suitSet(c)
	return !hasHonor
}

// meldContainsTerminalOrHonor 报告面子是否含幺九牌或字牌。
func meldContainsTerminalOrHonor(meld MeldShape) bool {
	for _, t := range meld.Tiles {
		if t.Kind.IsTerminalOrHonor() {
			return true
		}
	}
	return false
}

// meldContainsTerminal 报告面子是否含幺九数牌（字牌不算）。
func meldContainsTerminal(meld MeldShape) bool {
	for _, t := range meld.Tiles {
		if t.Kind.IsTerminal() {
			return true
		}
	}
	return false
}

// isChuuren 报告是否九莲宝灯：门清、单一花色、1112345678999 + 任意一张同花。
func isChuuren(shape WinShape, c counts) bool {
	if shape.Pattern != PatternStandard || !shapeMenzen(shape) {
		return false
	}
	suits, hasHonor := suitSet(c)
	if hasHonor || len(suits) != 1 {
		return false
	}
	base := 0
	for suit := tile.Man; suit <= tile.Sou; suit++ {
		if suits[suit] {
			base = int(suit) * 9
		}
	}
	if c[base] < 3 || c[base+8] < 3 {
		return false
	}
	for num := 2; num <= 8; num++ {
		if c[base+num-1] == 0 {
			return false
		}
	}
	return true
}

// validateShape 校验 WinShape 结构是否自洽（Evaluate/Fu 入口统一做一次）。
func validateShape(shape WinShape) error {
	switch shape.Pattern {
	case PatternStandard:
		if len(shape.Melds) != meldsRequired {
			return fmt.Errorf("%w: 标准型需要 4 组面子，实际 %d 组", ErrInvalidShape, len(shape.Melds))
		}
	case PatternSevenPairs, PatternKokushi:
		if len(shape.Melds) != 0 {
			return fmt.Errorf("%w: %s 不应有面子，实际 %d 组", ErrInvalidShape, shape.Pattern, len(shape.Melds))
		}
	default:
		return fmt.Errorf("%w: 未知牌型 %q", ErrInvalidShape, shape.Pattern)
	}
	if shape.Jantou.Kind >= tile.NumKinds {
		return fmt.Errorf("%w: 将牌 / 和了牌种类越界：%d", ErrInvalidShape, shape.Jantou.Kind)
	}
	switch shape.Wait {
	case WaitRyanmen, WaitKanchan, WaitPenchan, WaitShanpon, WaitTanki:
	default:
		return fmt.Errorf("%w: 未知待ち类型 %q", ErrInvalidShape, shape.Wait)
	}

	kan := 0
	total := len(shape.Tiles)
	for i, meld := range shape.Melds {
		isKan := len(meld.Tiles) == 4
		if isKan {
			kan++
		}
		if len(meld.Tiles) != 3 && len(meld.Tiles) != 4 {
			return fmt.Errorf("%w: 第 %d 组面子有 %d 张牌", ErrInvalidShape, i, len(meld.Tiles))
		}
		for _, t := range meld.Tiles {
			if t.Kind >= tile.NumKinds {
				return fmt.Errorf("%w: 第 %d 组面子含越界牌", ErrInvalidShape, i)
			}
		}
		switch meld.Kind {
		case MeldChi:
			if isKan || !isChiTiles(meld.Tiles) {
				return fmt.Errorf("%w: 第 %d 组面子不是合法顺子", ErrInvalidShape, i)
			}
		case MeldPon, MeldKanDaiminkan, MeldKanShouminkan, MeldKanAnkan:
			if !isPonTiles(meld.Tiles) {
				return fmt.Errorf("%w: 第 %d 组面子不是合法刻子/杠", ErrInvalidShape, i)
			}
		default:
			return fmt.Errorf("%w: 第 %d 组面子类型未知 %q", ErrInvalidShape, i, meld.Kind)
		}
	}

	// 若填写了 Tiles，则物理张数必须与牌型自洽（杠多一张）。
	if total > 0 {
		wantTotal := winHandSize
		if shape.Pattern == PatternStandard {
			wantTotal += kan
		}
		if total != wantTotal {
			return fmt.Errorf("%w: Tiles 有 %d 张，期望 %d 张", ErrInvalidShape, total, wantTotal)
		}
	}
	return nil
}

// validateContext 校验 ScoreContext 与和了方式是否自洽，并检查与 shape 的明暗是否一致。
func validateContext(shape WinShape, ctx ScoreContext) error {
	if ctx.SeatWind < tile.East || ctx.SeatWind > tile.North {
		return fmt.Errorf("%w: 自风必须是东南西北，实际 %v", ErrInvalidContext, ctx.SeatWind)
	}
	if ctx.RoundWind < tile.East || ctx.RoundWind > tile.North {
		return fmt.Errorf("%w: 场风必须是东南西北，实际 %v", ErrInvalidContext, ctx.RoundWind)
	}
	if ctx.Honba < 0 || ctx.RiichiSticks < 0 || ctx.DoraCount < 0 || ctx.AkaDora < 0 {
		return fmt.Errorf("%w: 本场/立直棒/宝牌数不能为负", ErrInvalidContext)
	}
	for _, ind := range ctx.DoraIndicators {
		if ind.Kind >= tile.NumKinds {
			return fmt.Errorf("%w: 宝牌指示牌种类越界", ErrInvalidContext)
		}
	}
	if ctx.Menzen != shapeMenzen(shape) {
		return fmt.Errorf("%w: ctx.Menzen=%v 与拆解结果的明暗不一致", ErrInvalidContext, ctx.Menzen)
	}
	if ctx.Riichi && !ctx.Menzen {
		return fmt.Errorf("%w: 立直必须门清", ErrInvalidContext)
	}
	if ctx.Ippatsu && !ctx.Riichi {
		return fmt.Errorf("%w: 一发必须伴随立直", ErrInvalidContext)
	}
	if ctx.Haitei && ctx.Houtei {
		return fmt.Errorf("%w: 海底与河底不能同时成立", ErrInvalidContext)
	}
	if ctx.Haitei && !ctx.Tsumo {
		return fmt.Errorf("%w: 海底摸月是自摸", ErrInvalidContext)
	}
	if ctx.Houtei && ctx.Tsumo {
		return fmt.Errorf("%w: 河底捞鱼是荣和", ErrInvalidContext)
	}
	if ctx.Rinshan && !ctx.Tsumo {
		return fmt.Errorf("%w: 岭上开花是自摸", ErrInvalidContext)
	}
	if ctx.Chankan && ctx.Tsumo {
		return fmt.Errorf("%w: 抢杠是荣和", ErrInvalidContext)
	}
	if ctx.Tenhou && (!ctx.Tsumo || ctx.SeatWind != tile.East) {
		return fmt.Errorf("%w: 天和必须是亲家自摸", ErrInvalidContext)
	}
	if ctx.Chiihou && (!ctx.Tsumo || ctx.SeatWind == tile.East) {
		return fmt.Errorf("%w: 地和必须是子家自摸", ErrInvalidContext)
	}
	return nil
}
