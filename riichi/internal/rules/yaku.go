package rules

import "summermahjong/riichi/internal/tile"

// 役种名称常量（中文），避免魔法字符串散落在 Evaluate / 测试 / 服务层里。
const (
	// 1 翻
	YakuRiichi       = "立直"
	YakuIppatsu      = "一发"
	YakuMenzenTsumo  = "门前清自摸和"
	YakuPinfu        = "平和"
	YakuTanyao       = "断幺九"
	YakuIipeikou     = "一盃口"
	YakuYakuhaiHaku  = "役牌·白"
	YakuYakuhaiHatsu = "役牌·发"
	YakuYakuhaiChun  = "役牌·中"
	YakuYakuhaiSeat  = "自风牌"
	YakuYakuhaiRound = "场风牌"
	YakuRinshan      = "岭上开花"
	YakuChankan      = "抢杠"
	YakuHaitei       = "海底摸月"
	YakuHoutei       = "河底捞鱼"

	// 2 翻
	YakuSanshokuDoujun = "三色同顺"
	YakuIttsu          = "一气通贯"
	YakuChanta         = "混全带幺九"
	YakuChiitoitsu     = "七对子"
	YakuToitoi         = "对对和"
	YakuSanankou       = "三暗刻"
	YakuSankantsu      = "三杠子"
	YakuShousangen     = "小三元"
	YakuHonroutou      = "混老头"
	YakuSanshokuDoukou = "三色同刻"
	YakuRyanpeikou     = "二盃口"
	YakuJunchan        = "纯全带幺九"
	YakuHonitsu        = "混一色"
	YakuChinitsu       = "清一色"
	YakuKokushi        = "国士无双"
	YakuSuuankou       = "四暗刻"
	YakuDaisangen      = "大三元"
	YakuShousuushii    = "小四喜"
	YakuDaisuushii     = "大四喜"
	YakuTsuuiisou      = "字一色"
	YakuRyuuiisou      = "绿一色"
	YakuChinroutou     = "清老头"
	YakuChuuren        = "九莲宝灯"
	YakuTenhou         = "天和"
	YakuChiihou        = "地和"
	YakuSuukantsu      = "四杠子"
)

// yakumanHan 是单个役满记的翻数（契约：役满按 13 翻/个编码，天凤默认单倍）。
const yakumanHan = 13

// Yaku 是一个役种（契约 §3）。
//
// Han 是役种本身的翻数；Yakuman=true 时按 13×YakumanMultiplier 计
// （天凤的双倍役满：国士十三面 / 四暗刻单骑 / 大四喜 / 純正九蓮 = 26 翻）。
// 宝牌/赤宝牌/里宝牌不是役种，不计入 Evaluate 的返回值，见 ScoreContext 的说明。
type Yaku struct {
	Name    string
	Han     int
	Yakuman bool
	// YakumanMultiplier 是役满倍数：1 或 2；非役满为 0。
	YakumanMultiplier int
}

// ScoreContext 是计分所需的场况与和了方式（契约 §3 / §8）。
//
// 调用方必须填对的字段：
//   - Menzen：是否门清（暗杠不影响门清）。必须与 WinShape 里面子的 Open 标记一致，
//     否则 Evaluate/Fu 返回 ErrInvalidContext；
//   - Tsumo / Riichi / Ippatsu / Rinshan / Chankan / Haitei / Houtei / Tenhou / Chiihou：
//     只在该条件成立时置 true（矛盾组合会被拒绝，例如一发但未立直、海底却荣和）；
//   - SeatWind / RoundWind：自风与场风，取 tile.East / South / West / North；
//   - Honba / RiichiSticks：本场数、立直棒数，用于 Score 的点数加成。
//
// 宝牌约定（防止重复计算，S5 务必遵守）：
//   - Evaluate 返回的 han **不含宝牌**；
//   - 表宝牌/赤宝牌可用 CountDora(shape, ctx) 统计，里宝牌由 S5 在立直和了时另算；
//   - 调用方把宝牌翻数加到 han 上，再调用 Score(han, fu, ctx)（Score 不会重复加）；
//   - DoraCount 是「宝牌翻数总和」的元数据字段，仅供展示/审计，Evaluate/Score 都不读取。
type ScoreContext struct {
	Menzen  bool
	Tsumo   bool
	Riichi  bool
	Ippatsu bool
	Rinshan bool
	Chankan bool
	Haitei  bool
	Houtei  bool
	Tenhou  bool
	Chiihou bool

	SeatWind  tile.Kind
	RoundWind tile.Kind

	Honba        int
	RiichiSticks int

	// DoraIndicators 是表宝牌指示牌（开局 1 张，每杠 +1，上限 5）。
	DoraIndicators []tile.Tile
	// AkaDora 是赤宝牌枚数（可用 CountAka(shape) 统计后填入）。
	AkaDora int
	// DoraCount 是宝牌翻数总和（含表/里/赤），仅元数据；Evaluate 不加、Score 不重复加。
	DoraCount int
}

// Evaluate 计算一手和牌的役种、翻数与符数（契约 §3）。
//
// 返回值：
//   - yaku：成立的役种列表（中文名、各自翻数）。出现役满时只返回役满役（天凤规则：
//     役满手不再累加普通役；多个役满可叠加，例如大三元+字一色）；
//   - han：yaku 的翻数之和。**不含宝牌**，宝牌由调用方统计后自己加上（见 ScoreContext）；
//   - fu：符数（与 Fu 相同）；
//   - err：shape 或 ctx 不自洽时返回 ErrInvalidShape / ErrInvalidContext。
//
// 算法：先判役满（国士/四暗刻/大三元/四喜/字一色/绿一色/清老头/九莲/四杠子/天和/地和），
// 再判普通役；普通役按「和了方式 → 牌型 → 花色系」三段累积。
// 纯全/混全/混一/清一/三色/一气按天凤的喰い下がり：门清时 +1 翻。
func Evaluate(shape WinShape, ctx ScoreContext) (yaku []Yaku, han int, fu int, err error) {
	if err := validateShape(shape); err != nil {
		return nil, 0, 0, err
	}
	if err := validateContext(shape, ctx); err != nil {
		return nil, 0, 0, err
	}
	fu, err = Fu(shape, ctx)
	if err != nil {
		return nil, 0, 0, err
	}

	if yakuman := evalYakuman(shape, ctx); len(yakuman) > 0 {
		for _, y := range yakuman {
			han += y.Han
		}
		return yakuman, han, fu, nil
	}

	normal := evalNormal(shape, ctx)
	for _, y := range normal {
		han += y.Han
	}
	return normal, han, fu, nil
}

// evalYakuman 判定役满（按固定顺序返回，便于测试与展示稳定）。
//
// 双倍役满（天凤规则）：
//   - 国士十三面：和了牌与手上的对子同种（c[winning]==2）；
//   - 四暗刻单骑：待ち类型是单骑（将牌即和了牌）；
//   - 大四喜：四组风刻；
//   - 純正九蓮：和牌前的手牌恰好是 1112345678999。
func evalYakuman(shape WinShape, ctx ScoreContext) []Yaku {
	c := shapeCounts(shape)
	out := make([]Yaku, 0, 3)
	add := func(name string, multiplier int) {
		if multiplier < 1 {
			multiplier = 1
		}
		out = append(out, Yaku{
			Name:              name,
			Han:               yakumanHan * multiplier,
			Yakuman:           true,
			YakumanMultiplier: multiplier,
		})
	}

	// 天和 / 地和与牌型无关，但只可能出现在自摸第一巡（validateContext 已校验）。
	if ctx.Tenhou {
		add(YakuTenhou, 1)
	}
	if ctx.Chiihou {
		add(YakuChiihou, 1)
	}

	if shape.Pattern == PatternKokushi {
		multiplier := 1
		if kokushiThirteenSided(shape, c) {
			multiplier = 2
		}
		add(YakuKokushi, multiplier)
		return out
	}

	// 字一色：全字牌（七对形也可成立）。
	if allHonors(c) {
		add(YakuTsuuiisou, 1)
	}
	if shape.Pattern == PatternSevenPairs {
		return out
	}

	if allTerminals(c) {
		add(YakuChinroutou, 1)
	}
	if allGreen(c) {
		add(YakuRyuuiisou, 1)
	}
	if isChuuren(shape, c) {
		multiplier := 1
		if isJunseiChuuren(shape, c) {
			multiplier = 2
		}
		add(YakuChuuren, multiplier)
	}
	if shapeMenzen(shape) && concealedPonCount(shape) == meldsRequired {
		multiplier := 1
		if shape.Wait == WaitTanki {
			multiplier = 2
		}
		add(YakuSuuankou, multiplier)
	}
	if dragonTripletCount(shape) == 3 {
		add(YakuDaisangen, 1)
	}
	switch winds := windTripletCount(shape); {
	case winds == 4:
		add(YakuDaisuushii, 2)
	case winds == 3 && shape.Jantou.Kind.IsWind():
		add(YakuShousuushii, 1)
	}
	if kanCount(shape) == meldsRequired {
		add(YakuSuukantsu, 1)
	}
	return out
}

// kokushiThirteenSided 报告国士是否十三面听：成和时和了牌与手上的对子同种。
//
// 十三面：和牌前 13 种幺九字牌各一张，和了牌补成对子（c[winning]==2）；
// 单面：和牌前缺一种、已有对子，和了牌补的是缺的那一张（c[winning]==1）。
func kokushiThirteenSided(shape WinShape, c counts) bool {
	kind := shape.WinningTile.Kind
	if kind >= tile.NumKinds {
		return false
	}
	return c[kind] == 2
}

// isJunseiChuuren 报告是否純正九蓮宝燈：把和了牌从计数里去掉后，恰好是 1112345678999。
func isJunseiChuuren(shape WinShape, c counts) bool {
	if !isChuuren(shape, c) {
		return false
	}
	winning := int(shape.WinningTile.Kind)
	if winning >= tile.NumKinds || c[winning] == 0 {
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
	if winning < base || winning >= base+9 {
		return false
	}
	pre := c
	pre[winning]--
	want := [9]uint8{3, 1, 1, 1, 1, 1, 1, 1, 3}
	for i := 0; i < 9; i++ {
		if pre[base+i] != want[i] {
			return false
		}
	}
	return true
}

// evalNormal 判定普通役（不含役满；调用前已确认没有役满）。
func evalNormal(shape WinShape, ctx ScoreContext) []Yaku {
	c := shapeCounts(shape)
	menzen := ctx.Menzen
	menzenHan := 0
	if menzen {
		menzenHan = 1
	}
	out := make([]Yaku, 0, 8)
	add := func(name string, han int) {
		if han > 0 {
			out = append(out, Yaku{Name: name, Han: han})
		}
	}

	// 1. 和了方式相关（立直/一发/门清自摸/岭上/抢杠/海底/河底）。
	if ctx.Riichi {
		add(YakuRiichi, 1)
	}
	if ctx.Ippatsu {
		add(YakuIppatsu, 1)
	}
	if menzen && ctx.Tsumo {
		add(YakuMenzenTsumo, 1)
	}
	if ctx.Rinshan {
		add(YakuRinshan, 1)
	}
	if ctx.Chankan {
		add(YakuChankan, 1)
	}
	if ctx.Haitei {
		add(YakuHaitei, 1)
	}
	if ctx.Houtei {
		add(YakuHoutei, 1)
	}

	// 2. 牌型相关。
	switch shape.Pattern {
	case PatternSevenPairs:
		add(YakuChiitoitsu, 2)
		if allTerminalOrHonor(c) {
			add(YakuHonroutou, 2)
		}
	case PatternStandard:
		if pinfuShape(shape, ctx) {
			add(YakuPinfu, 1)
		}
		if allSimple(c) {
			add(YakuTanyao, 1)
		}
		if menzen {
			switch chiPairCount(shape) {
			case 1:
				add(YakuIipeikou, 1)
			case 2:
				add(YakuRyanpeikou, 3)
			}
		}
		// 役牌：三元牌各 1 翻；自风/场风各 1 翻（连风两个都算，共 2 翻）。
		for _, meld := range ponLikeMelds(shape) {
			kind := meld.Tiles[0].Kind
			switch kind {
			case tile.Haku:
				add(YakuYakuhaiHaku, 1)
			case tile.Hatsu:
				add(YakuYakuhaiHatsu, 1)
			case tile.Chun:
				add(YakuYakuhaiChun, 1)
			}
			if kind == ctx.SeatWind {
				add(YakuYakuhaiSeat, 1)
			}
			if kind == ctx.RoundWind {
				add(YakuYakuhaiRound, 1)
			}
		}
		if sanshokuDoujun(shape) {
			add(YakuSanshokuDoujun, 1+menzenHan)
		}
		if ittsu(shape) {
			add(YakuIttsu, 1+menzenHan)
		}
		if junchan(shape, c) {
			add(YakuJunchan, 2+menzenHan)
		} else if chanta(shape, c) {
			add(YakuChanta, 1+menzenHan)
		}
		if allPonShape(shape) {
			add(YakuToitoi, 2)
		}
		if concealedPonCount(shape) >= 3 {
			add(YakuSanankou, 2)
		}
		if kanCount(shape) == 3 {
			add(YakuSankantsu, 2)
		}
		if dragonTripletCount(shape) == 2 && shape.Jantou.Kind.IsDragon() {
			add(YakuShousangen, 2)
		}
		if allPonShape(shape) && allTerminalOrHonor(c) {
			add(YakuHonroutou, 2)
		}
		if sanshokuDoukou(shape) {
			add(YakuSanshokuDoukou, 2)
		}
	}

	// 3. 花色系（标准型 / 七对形都适用；字一色已在役满分支返回）。
	if isChinitsu(c) {
		add(YakuChinitsu, 5+menzenHan)
	} else if isHonitsu(c) {
		add(YakuHonitsu, 2+menzenHan)
	}
	return out
}
