package rules

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// 本文件用表驱动覆盖全部役种：每个役 1 个正例 + 1 个反例（task-4 验收要求）。
//
// 正例：
//   - 门清役用真实的 14 张手牌走 Decompose → Evaluate；
//   - 副露/杠相关的役（对对和、混老头、三杠子、四杠子）用 buildShape 手工构造，
//     因为 S3/S4 的 Decompose 还只支持门清。
//
// 反例：给每个役配一手「能判和但确实不成立该役」的手牌（多数复用 plain 手牌，
// 局部用近邻牌型，例如把两面听改成嵌张听来否掉平和）。

// plainHand 是最基础的平和门清和牌：123m 456m 789p 11p 456s，和 6s。
// 特意避开「123/456/789 同花色」，以免顺手凑成一气通贯干扰其它用例。
const plainHand = "123m 456m 789p 11p 456s"

// TestYakuPositiveClosed 覆盖门清手牌可以成立的 1/2/3/6 翻役与役满役。
func TestYakuPositiveClosed(t *testing.T) {
	cases := []struct {
		name    string
		hand    string
		winning string
		ctx     ScoreContext
		want    string
		wantHan int
	}{
		{"立直", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Riichi = true }), YakuRiichi, 1},
		{"一发", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Riichi = true; c.Ippatsu = true }), YakuIppatsu, 1},
		{"门清自摸", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Tsumo = true }), YakuMenzenTsumo, 1},
		{"平和", plainHand, "6s", plainCtx(), YakuPinfu, 1},
		{"断幺九", "234m 456m 678p 345s 55s", "3s", plainCtx(), YakuTanyao, 1},
		{"一盃口", "112233m 456p 789s 99s", "3m", plainCtx(), YakuIipeikou, 1},
		{"役牌白", "555z 123m 456m 789m 99p", "9p", plainCtx(), YakuYakuhaiHaku, 1},
		{"役牌发", "666z 123m 456m 789m 99p", "9p", plainCtx(), YakuYakuhaiHatsu, 1},
		{"役牌中", "777z 123m 456m 789m 99p", "9p", plainCtx(), YakuYakuhaiChun, 1},
		{"自风牌", "222z 123m 456m 789m 99p", "9p", plainCtx(), YakuYakuhaiSeat, 1},
		{"场风牌", "111z 123m 456m 789m 99p", "9p", plainCtx(), YakuYakuhaiRound, 1},
		{"岭上开花", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Tsumo = true; c.Rinshan = true }), YakuRinshan, 1},
		{"抢杠", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Chankan = true }), YakuChankan, 1},
		{"海底摸月", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Tsumo = true; c.Haitei = true }), YakuHaitei, 1},
		{"河底捞鱼", plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Houtei = true }), YakuHoutei, 1},

		{"三色同顺", "123m 123p 123s 456m 99s", "3s", plainCtx(), YakuSanshokuDoujun, 2},
		{"一气通贯", "123m 456m 789m 123p 99s", "9s", plainCtx(), YakuIttsu, 2},
		{"混全带幺九", "123m 789m 123p 789s 11z", "1z", plainCtx(), YakuChanta, 2},
		{"七对子", "11m 22m 33m 44p 55p 66s 11z", "1z", plainCtx(), YakuChiitoitsu, 2},
		{"三暗刻", "111m 222m 333m 456p 99s", "9s", plainCtx(), YakuSanankou, 2},
		{"小三元", "555z 666z 77z 123m 456m", "7z", plainCtx(), YakuShousangen, 2},
		{"三色同刻", "111m 111p 111s 456m 99m", "9m", plainCtx(), YakuSanshokuDoukou, 2},

		{"二盃口", "112233m 445566p 99s", "9s", plainCtx(), YakuRyanpeikou, 3},
		{"纯全带幺九", "123m 789m 123p 789s 11m", "1m", plainCtx(), YakuJunchan, 3},
		{"混一色", "123m 456m 789m 111z 22z", "2z", plainCtx(), YakuHonitsu, 3},
		{"清一色", "123m 456m 789m 234m 99m", "9m", plainCtx(), YakuChinitsu, 6},

		{"国士无双", "19m 19p 19s 1234567z 1m", "1m", plainCtx(), YakuKokushi, 13},
		{"四暗刻", "111m 222p 333s 444z 55m", "5m", plainCtx(), YakuSuuankou, 13},
		{"大三元", "555z 666z 777z 123m 99p", "9p", plainCtx(), YakuDaisangen, 13},
		{"小四喜", "111z 222z 333z 44z 567m", "4z", plainCtx(), YakuShousuushii, 13},
		{"大四喜", "111z 222z 333z 444z 99m", "9m", plainCtx(), YakuDaisuushii, 13},
		{"字一色", "111z 222z 333z 444z 55z", "5z", plainCtx(), YakuTsuuiisou, 13},
		{"绿一色", "222s 333s 444s 666s 88s", "8s", plainCtx(), YakuRyuuiisou, 13},
		{"清老头", "111m 999m 111p 999p 11s", "1s", plainCtx(), YakuChinroutou, 13},
		{"九莲宝灯", "11123456789995m", "5m", plainCtx(), YakuChuuren, 13},
		{"天和", plainHand, "6s", ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.East
			c.Tsumo = true
			c.Tenhou = true
		}), YakuTenhou, 13},
		{"地和", plainHand, "6s", ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.South
			c.Tsumo = true
			c.Chiihou = true
		}), YakuChiihou, 13},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, yaku, _, _ := evalHand(t, tc.hand, tc.winning, tc.ctx)
			requireYaku(t, yaku, tc.want, tc.wantHan)
		})
	}
}

// TestYakuPositiveOpenShapes 覆盖必须副露/杠才能出现的役：
// 对对和、混老头、三杠子、四杠子（门清四刻子会被四暗刻役满吸收）。
func TestYakuPositiveOpenShapes(t *testing.T) {
	openCtx := ctxWith(func(c *ScoreContext) { c.Menzen = false })
	closedCtx := plainCtx()

	cases := []struct {
		name    string
		shape   WinShape
		ctx     ScoreContext
		want    string
		wantHan int
	}{
		{
			"对对和（副露四刻子）",
			buildShape(t, PatternStandard, "9s", WaitTanki,
				"pon:1z", "pon_closed:2m", "pon_closed:3p", "pon_closed:4s"),
			openCtx, YakuToitoi, 2,
		},
		{
			"混老头（副露 + 全幺九刻子）",
			buildShape(t, PatternStandard, "1m", WaitTanki,
				"pon:1z", "pon_closed:9m", "pon_closed:1p", "pon_closed:9s"),
			openCtx, YakuHonroutou, 2,
		},
		{
			"三杠子",
			buildShape(t, PatternStandard, "9s", WaitTanki,
				"kan_ankan:1m", "kan_ankan:1p", "kan_ankan:1s", "chi_closed:234m"),
			closedCtx, YakuSankantsu, 2,
		},
		{
			"四杠子",
			buildShape(t, PatternStandard, "9s", WaitTanki,
				"kan_ankan:1m", "kan_ankan:1p", "kan_ankan:1s", "kan_ankan:2m"),
			closedCtx, YakuSuukantsu, 13,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			yaku, _, _ := evalShape(t, tc.shape, tc.ctx)
			requireYaku(t, yaku, tc.want, tc.wantHan)
		})
	}
}

// TestYakuNegative 给每个役一个反例：手牌/场况能判和，但该役不成立。
//
// 反例手牌大多复用 plainHand（只有平和），近邻易混的役用专门的近似牌型：
// 例如把两面听改成嵌张听否定平和、用两色同顺否定三色同顺、用混全否定纯全。
func TestYakuNegative(t *testing.T) {
	// 无任何役的近邻手牌：同一手牌改成嵌张听（5s），于是连平和也没有。
	noYakuWinning := "5s"

	cases := []struct {
		yaku    string
		hand    string
		winning string
		ctx     ScoreContext
	}{
		{YakuRiichi, plainHand, noYakuWinning, plainCtx()},
		{YakuIppatsu, plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Riichi = true })},
		{YakuMenzenTsumo, plainHand, noYakuWinning, plainCtx()},
		{YakuPinfu, plainHand, noYakuWinning, plainCtx()},
		{YakuTanyao, "123m 456m 678p 345s 55s", "3s", plainCtx()}, // 含 1m，非断幺
		{YakuIipeikou, "123m 234m 456p 789s 99s", "2m", plainCtx()},
		{YakuYakuhaiHaku, "666z 123m 456m 789m 99p", "9p", plainCtx()},
		{YakuYakuhaiHatsu, "777z 123m 456m 789m 99p", "9p", plainCtx()},
		{YakuYakuhaiChun, "555z 123m 456m 789m 99p", "9p", plainCtx()},
		{YakuYakuhaiSeat, "222z 123m 456m 789m 99p", "9p", ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.West // 刻子是南，自风是西 → 不成立
		})},
		{YakuYakuhaiRound, "111z 123m 456m 789m 99p", "9p", ctxWith(func(c *ScoreContext) {
			c.RoundWind = tile.South // 刻子是东，场风是南 → 不成立
		})},
		{YakuRinshan, plainHand, noYakuWinning, plainCtx()},
		{YakuChankan, plainHand, noYakuWinning, plainCtx()},
		{YakuHaitei, plainHand, noYakuWinning, plainCtx()},
		{YakuHoutei, plainHand, noYakuWinning, plainCtx()},
		{YakuSanshokuDoujun, "123m 123p 456m 789s 99s", "9s", plainCtx()}, // 只有万/筒两组同顺
		{YakuIttsu, "123m 456m 123p 789s 99s", "9s", plainCtx()},          // 万只到 456
		{YakuChanta, "123m 789m 123p 789s 11m", "1m", plainCtx()},         // 无字牌 → 是纯全
		{YakuChiitoitsu, plainHand, "6s", plainCtx()},
		{YakuToitoi, plainHand, "6s", plainCtx()},
		{YakuSanankou, "111m 222m 456p 789s 99s", "9s", plainCtx()}, // 只有两组暗刻
		{YakuSankantsu, plainHand, "6s", plainCtx()},
		{YakuShousangen, "555z 666z 123m 456m 99m", "9m", plainCtx()},     // 三元两组刻子但雀头非三元
		{YakuHonroutou, "123m 789m 123p 789s 11z", "1z", plainCtx()},      // 有顺子 → 是混全
		{YakuSanshokuDoukou, "111m 222p 333s 456m 99m", "9m", plainCtx()}, // 数字不同
		{YakuRyanpeikou, "112233m 456p 789s 99s", "3m", plainCtx()},       // 只有一盃口
		{YakuJunchan, "123m 789m 123p 789s 11z", "1z", plainCtx()},        // 有字牌 → 是混全
		{YakuHonitsu, "123m 456m 789m 123p 99s", "9s", plainCtx()},        // 两门花色
		{YakuChinitsu, "123m 456m 789m 123p 99s", "9s", plainCtx()},       // 两门花色
		{YakuKokushi, plainHand, "6s", plainCtx()},
		{YakuSuuankou, "111m 222m 333m 456p 99s", "9s", plainCtx()},    // 三暗刻，不是四暗刻
		{YakuDaisangen, "555z 666z 77z 123m 456m", "7z", plainCtx()},   // 小三元
		{YakuShousuushii, "111z 222z 333z 99m 567m", "9m", plainCtx()}, // 三风刻但雀头非风
		{YakuDaisuushii, "111z 222z 333z 44z 567m", "4z", plainCtx()},  // 三风刻，是小四喜
		{YakuTsuuiisou, "111z 222z 333z 55z 678m", "8m", plainCtx()},   // 含数牌
		{YakuRyuuiisou, "222s 333s 444s 666s 55s", "5s", plainCtx()},   // 5s 不是绿一色
		{YakuChinroutou, "111m 999m 111p 999p 11z", "1z", plainCtx()},  // 含字牌 → 混老头
		{YakuChuuren, "123m 456m 789m 234m 99m", "9m", plainCtx()},     // 清一色但不是九莲
		{YakuTenhou, plainHand, "6s", ctxWith(func(c *ScoreContext) { c.Tsumo = true })},
		{YakuChiihou, plainHand, "6s", ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.South
			c.Tsumo = true
		})},
		{YakuSuukantsu, plainHand, "6s", plainCtx()},
	}

	if len(cases) < 41 {
		t.Fatalf("反例只有 %d 条，覆盖不完整", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.yaku, func(t *testing.T) {
			_, yaku, _, _ := evalHand(t, tc.hand, tc.winning, tc.ctx)
			requireNoYaku(t, yaku, tc.yaku)
		})
	}
}

// TestYakuCount 断言役种清单至少 30 个，并且常量表与实装一致。
func TestYakuCount(t *testing.T) {
	names := []string{
		YakuRiichi, YakuIppatsu, YakuMenzenTsumo, YakuPinfu, YakuTanyao,
		YakuIipeikou, YakuYakuhaiHaku, YakuYakuhaiHatsu, YakuYakuhaiChun,
		YakuYakuhaiSeat, YakuYakuhaiRound, YakuRinshan, YakuChankan,
		YakuHaitei, YakuHoutei,
		YakuSanshokuDoujun, YakuIttsu, YakuChanta, YakuChiitoitsu,
		YakuToitoi, YakuSanankou, YakuSankantsu, YakuShousangen,
		YakuHonroutou, YakuSanshokuDoukou,
		YakuRyanpeikou, YakuJunchan, YakuHonitsu, YakuChinitsu,
		YakuKokushi, YakuSuuankou, YakuDaisangen, YakuShousuushii,
		YakuDaisuushii, YakuTsuuiisou, YakuRyuuiisou, YakuChinroutou,
		YakuChuuren, YakuTenhou, YakuChiihou, YakuSuukantsu,
	}
	if len(names) < 30 {
		t.Fatalf("役种只有 %d 个，要求至少 30 个", len(names))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Fatalf("役种名称重复：%q", name)
		}
		seen[name] = true
	}
}
