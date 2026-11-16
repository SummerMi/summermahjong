package rules

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// 25 手金标：每手写死「役（部分校验）/ 翻 / 符 / 点数」，期望值按日麻标准点数表手算。
//
// 约定：
//   - 点数表来自天凤规则：切り上げ満貫なし，25 符七对固定，平和自摸 20 符等；
//   - wantFu = -1 表示该手是役满（符不参与点数，跳过断言）；
//   - han 断言的是「含宝牌的最终翻数」，与 Score 的入参一致；
//   - 宝牌/赤宝牌翻数由 CountDora 统计后加到 Evaluate 的 han 上（与 S5 的调用方式一致）。
type goldenCase struct {
	name       string
	hand       string
	winning    string
	ctx        ScoreContext
	wantYaku   []string
	wantHan    int
	wantFu     int
	wantRon    int
	wantChild  int
	wantDealer int
	wantTotal  int
	red        bool // 手牌里含一张赤 5s（用于赤宝牌用例）
}

func TestGoldenHands(t *testing.T) {
	child := plainCtx()
	dealer := ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East })
	childTsumo := ctxWith(func(c *ScoreContext) { c.Tsumo = true })
	dealerTsumo := ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East; c.Tsumo = true })
	riichiCtx := ctxWith(func(c *ScoreContext) { c.Riichi = true })
	riichiTsumo := ctxWith(func(c *ScoreContext) { c.Riichi = true; c.Tsumo = true })

	cases := []goldenCase{
		{
			name: "平和自摸（子，20 符 2 翻）", hand: plainHand, winning: "6s", ctx: childTsumo,
			wantYaku: []string{YakuPinfu, YakuMenzenTsumo}, wantHan: 2, wantFu: 20,
			wantChild: 400, wantDealer: 700, wantTotal: 1500,
		},
		{
			name: "平和自摸（亲，700 all）", hand: plainHand, winning: "6s", ctx: dealerTsumo,
			wantYaku: []string{YakuPinfu, YakuMenzenTsumo}, wantHan: 2, wantFu: 20,
			wantChild: 700, wantDealer: 700, wantTotal: 2100,
		},
		{
			name: "立直+平和 荣和（子，2000）", hand: plainHand, winning: "6s", ctx: riichiCtx,
			wantYaku: []string{YakuRiichi, YakuPinfu}, wantHan: 2, wantFu: 30, wantRon: 2000, wantTotal: 2000,
		},
		{
			name: "立直+平和+门清自摸（子，700/1300）", hand: plainHand, winning: "6s", ctx: riichiTsumo,
			wantYaku: []string{YakuRiichi, YakuPinfu, YakuMenzenTsumo}, wantHan: 3, wantFu: 20,
			wantChild: 700, wantDealer: 1300, wantTotal: 2700,
		},
		{
			name: "立直+一发+平和（亲，5800）", hand: plainHand, winning: "6s",
			ctx:      ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East; c.Riichi = true; c.Ippatsu = true }),
			wantYaku: []string{YakuRiichi, YakuIppatsu, YakuPinfu}, wantHan: 3, wantFu: 30,
			wantRon: 5800, wantTotal: 5800,
		},
		{
			name: "七对子 荣和（子，25 符 1600）", hand: "11m 22m 33m 44p 55p 66s 11z", winning: "1z", ctx: child,
			wantYaku: []string{YakuChiitoitsu}, wantHan: 2, wantFu: 25, wantRon: 1600, wantTotal: 1600,
		},
		{
			name: "七对+立直 荣和（子，3200）", hand: "11m 22m 33m 44p 55p 66s 11z", winning: "1z", ctx: riichiCtx,
			wantYaku: []string{YakuChiitoitsu, YakuRiichi}, wantHan: 3, wantFu: 25, wantRon: 3200, wantTotal: 3200,
		},
		{
			name: "七对+立直+门清自摸（子，1600/3200）", hand: "11m 22m 33m 44p 55p 66s 11z", winning: "1z",
			ctx:      riichiTsumo,
			wantYaku: []string{YakuChiitoitsu, YakuRiichi, YakuMenzenTsumo}, wantHan: 4, wantFu: 25,
			wantChild: 1600, wantDealer: 3200, wantTotal: 6400,
		},
		{
			name: "断幺+平和+门清自摸（子，700/1300）", hand: "234m 456m 678p 345s 55s", winning: "3s", ctx: childTsumo,
			wantYaku: []string{YakuTanyao, YakuPinfu, YakuMenzenTsumo}, wantHan: 3, wantFu: 20,
			wantChild: 700, wantDealer: 1300, wantTotal: 2700,
		},
		{
			name: "三色同顺（子，40 符 2600）", hand: "123m 123p 123s 456m 99s", winning: "3s", ctx: child,
			wantYaku: []string{YakuSanshokuDoujun}, wantHan: 2, wantFu: 40, wantRon: 2600, wantTotal: 2600,
		},
		{
			name: "一气通贯（子，单骑 40 符 2600）", hand: "123m 456m 789m 123p 99s", winning: "9s", ctx: child,
			wantYaku: []string{YakuIttsu}, wantHan: 2, wantFu: 40, wantRon: 2600, wantTotal: 2600,
		},
		{
			name: "混一色+一气+立直+场风（子，跳满 12000）", hand: "123m 456m 789m 111z 22z", winning: "2z", ctx: riichiCtx,
			wantYaku: []string{YakuHonitsu, YakuYakuhaiRound, YakuRiichi, YakuIttsu}, wantHan: 7, wantFu: 50,
			wantRon: 12000, wantTotal: 12000,
		},
		{
			// 13 张听牌形是 123m 234m 456m 78m 99m：9m 既能补 789（两面 6/9），
			// 也能成 99 单骑（789+9m）。按包内文档化的「两面优先」取平和，故 11 翻 = 三倍满。
			name: "清一色+一气+立直+门清自摸+平和（子，三倍满 24000）", hand: "123m 456m 789m 234m 99m", winning: "9m",
			ctx: riichiTsumo, wantHan: 11, wantFu: 20,
			wantChild: 6000, wantDealer: 12000, wantTotal: 24000,
		},
		{
			name: "国士十三面（子，双倍 64000）", hand: "19m 19p 19s 1234567z 1m", winning: "1m", ctx: child,
			wantYaku: []string{YakuKokushi}, wantHan: 26, wantFu: 25, wantRon: 64000, wantTotal: 64000,
		},
		{
			name: "国士十三面（亲，双倍 96000）", hand: "19m 19p 19s 1234567z 1m", winning: "1m", ctx: dealer,
			wantYaku: []string{YakuKokushi}, wantHan: 26, wantFu: 25, wantRon: 96000, wantTotal: 96000,
		},
		{
			name: "四暗刻单骑（子，双倍 64000）", hand: "111m 222p 333s 444z 55m", winning: "5m", ctx: child,
			wantYaku: []string{YakuSuuankou}, wantHan: 26, wantFu: -1, wantRon: 64000, wantTotal: 64000,
		},
		{
			name: "四暗刻单骑 自摸（亲，32000 all = 96000）", hand: "111m 222p 333s 444z 55m", winning: "5m",
			ctx: dealerTsumo, wantYaku: []string{YakuSuuankou}, wantHan: 26, wantFu: -1,
			wantChild: 32000, wantDealer: 32000, wantTotal: 96000,
		},
		{
			name: "国士单面（子，单倍 32000）", hand: "19m 19p 19s 123456z 1m 7z", winning: "7z", ctx: child,
			wantYaku: []string{YakuKokushi}, wantHan: 13, wantFu: 25, wantRon: 32000, wantTotal: 32000,
		},
		{
			name: "九莲宝灯 单面（子，单倍 32000）", hand: "11122345678999m", winning: "9m", ctx: child,
			wantYaku: []string{YakuChuuren}, wantHan: 13, wantFu: -1, wantRon: 32000, wantTotal: 32000,
		},
		{
			name: "純正九蓮宝燈（子，双倍 64000）", hand: "11123456789995m", winning: "5m", ctx: child,
			wantYaku: []string{YakuChuuren}, wantHan: 26, wantFu: -1, wantRon: 64000, wantTotal: 64000,
		},
		{
			name: "大四喜+四暗刻单骑（子，4 倍役满 128000）", hand: "111z 222z 333z 444z 99m", winning: "9m", ctx: child,
			wantYaku: []string{YakuSuuankou, YakuDaisuushii}, wantHan: 52, wantFu: -1, wantRon: 128000, wantTotal: 128000,
		},
		{
			name: "大三元（子，32000）", hand: "555z 666z 777z 123m 99p", winning: "9p", ctx: child,
			wantYaku: []string{YakuDaisangen}, wantHan: 13, wantFu: -1, wantRon: 32000, wantTotal: 32000,
		},
		{
			name: "字一色+七对（子，32000）", hand: "11z 22z 33z 44z 55z 66z 77z", winning: "1z", ctx: child,
			wantYaku: []string{YakuTsuuiisou}, wantHan: 13, wantFu: 25, wantRon: 32000, wantTotal: 32000,
		},
		{
			name: "绿一色（子，32000）", hand: "234s 234s 666s 888s 22s", winning: "4s", ctx: child,
			wantYaku: []string{YakuRyuuiisou}, wantHan: 13, wantFu: -1, wantRon: 32000, wantTotal: 32000,
		},
		{
			name: "小四喜（子，32000）", hand: "111z 222z 333z 44z 567m", winning: "4z", ctx: child,
			wantYaku: []string{YakuShousuushii}, wantHan: 13, wantFu: -1, wantRon: 32000, wantTotal: 32000,
		},
		{
			name: "混一+一气+立直+场风 + 2 本场 + 1 立直棒（子，13600）", hand: "123m 456m 789m 111z 22z", winning: "2z",
			ctx: ctxWith(func(c *ScoreContext) {
				c.Riichi = true
				c.Honba = 2
				c.RiichiSticks = 1
			}),
			wantYaku: []string{YakuHonitsu, YakuYakuhaiRound, YakuRiichi, YakuIttsu}, wantHan: 7, wantFu: 50,
			wantRon: 13600, wantTotal: 13600,
		},
		{
			name: "立直+平和+宝牌 3（子，满贯 8000）", hand: plainHand, winning: "6s",
			ctx: ctxWith(func(c *ScoreContext) {
				c.Riichi = true
				c.DoraIndicators = []tile.Tile{tileFromName(t, "5s"), tileFromName(t, "9p")}
			}),
			wantYaku: []string{YakuRiichi, YakuPinfu}, wantHan: 5, wantFu: 30, wantRon: 8000, wantTotal: 8000,
		},
		{
			name: "立直+平和+赤宝牌 1（子，3900）", hand: plainHand, winning: "6s", ctx: riichiCtx,
			wantYaku: []string{YakuRiichi, YakuPinfu}, wantHan: 3, wantFu: 30, wantRon: 3900, wantTotal: 3900, red: true,
		},
		{
			name: "三色+立直（亲，7700）", hand: "123m 123p 123s 456m 99s", winning: "3s",
			ctx:      ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East; c.Riichi = true }),
			wantYaku: []string{YakuSanshokuDoujun, YakuRiichi}, wantHan: 3, wantFu: 40,
			wantRon: 7700, wantTotal: 7700,
		},
		{
			name: "平和+门清自摸+赤宝牌 1（子，700/1300）", hand: plainHand, winning: "6s", ctx: childTsumo, red: true,
			wantYaku: []string{YakuPinfu, YakuMenzenTsumo}, wantHan: 3, wantFu: 20,
			wantChild: 700, wantDealer: 1300, wantTotal: 2700,
		},
		{
			name: "立直+平和 + 3 根立直棒（子，5000）", hand: plainHand, winning: "6s",
			ctx: ctxWith(func(c *ScoreContext) {
				c.Riichi = true
				c.RiichiSticks = 3
			}),
			wantYaku: []string{YakuRiichi, YakuPinfu}, wantHan: 2, wantFu: 30, wantRon: 5000, wantTotal: 5000,
		},
		{
			name: "国士十三面 + 1 本场（亲，96300）", hand: "19m 19p 19s 1234567z 1m", winning: "1m",
			ctx:      ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East; c.Honba = 1 }),
			wantYaku: []string{YakuKokushi}, wantHan: 26, wantFu: 25, wantRon: 96300, wantTotal: 96300,
		},
		{
			name: "七对+宝牌 2（子，25 符 4 翻 6400）", hand: "11m 22m 33m 44p 55p 66s 11z", winning: "1z",
			ctx: ctxWith(func(c *ScoreContext) {
				c.DoraIndicators = []tile.Tile{tileFromName(t, "1m")} // 指示牌 1m → 宝牌 2m（手里 2 张）
			}),
			wantYaku: []string{YakuChiitoitsu}, wantHan: 4, wantFu: 25, wantRon: 6400, wantTotal: 6400,
		},
	}

	if len(cases) < 25 {
		t.Fatalf("金标用例只有 %d 手，要求至少 25 手", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			winning := tileFromName(t, tc.winning)

			// 赤宝牌用例：把手里一张 5s 换成赤 5s，并按 S5 的方式把赤宝牌数填进 ctx。
			ctx := tc.ctx
			if tc.red {
				red5s := tileFromName(t, "r5s")
				for i := range hand {
					if hand[i].Kind == tile.Sou5 {
						hand[i] = red5s
						break
					}
				}
			}

			shape, ok := Decompose(hand, nil, winning)
			if !ok {
				t.Fatalf("Decompose(%q, winning=%v) 应判和", tc.hand, winning)
			}
			if tc.red {
				ctx.AkaDora = CountAka(shape)
			}
			yaku, han, fu, err := Evaluate(shape, ctx)
			if err != nil {
				t.Fatalf("Evaluate 返回错误：%v", err)
			}
			dora := CountDora(shape, ctx)
			finalHan := han + dora
			if finalHan != tc.wantHan {
				t.Fatalf("翻数 = %d（役种 %d + 宝牌 %d），期望 %d（役种：%v）",
					finalHan, han, dora, tc.wantHan, yakuNames(yaku))
			}
			if tc.wantFu >= 0 && fu != tc.wantFu {
				t.Fatalf("符数 = %d，期望 %d（役种：%v）", fu, tc.wantFu, yakuNames(yaku))
			}
			for _, want := range tc.wantYaku {
				if _, ok := findYaku(yaku, want); !ok {
					t.Fatalf("缺少役种 %q，实际役种：%v", want, yakuNames(yaku))
				}
			}

			pts := Score(finalHan, fu, ctx)
			if pts.Ron != tc.wantRon {
				t.Fatalf("Ron = %d，期望 %d", pts.Ron, tc.wantRon)
			}
			if pts.TsumoChild != tc.wantChild || pts.TsumoDealer != tc.wantDealer {
				t.Fatalf("自摸支付 = %d/%d，期望 %d/%d",
					pts.TsumoChild, pts.TsumoDealer, tc.wantChild, tc.wantDealer)
			}
			if pts.Total != tc.wantTotal {
				t.Fatalf("Total = %d，期望 %d", pts.Total, tc.wantTotal)
			}
		})
	}
}
