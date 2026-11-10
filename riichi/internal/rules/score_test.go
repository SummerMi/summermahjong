package rules

import (
	"errors"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestFu 覆盖符计算的全部加符项与特例。
func TestFu(t *testing.T) {
	plain := func(t *testing.T) WinShape {
		t.Helper()
		shape, ok := Decompose(mustParseHand(t, plainHand), nil, tileFromName(t, "6s"))
		if !ok {
			t.Fatal("plainHand 应判和")
		}
		return shape
	}
	kanchan := func(t *testing.T) WinShape {
		t.Helper()
		shape, ok := Decompose(mustParseHand(t, plainHand), nil, tileFromName(t, "5s"))
		if !ok {
			t.Fatal("嵌张手牌应判和")
		}
		return shape
	}

	cases := []struct {
		name  string
		shape func(*testing.T) WinShape
		ctx   ScoreContext
		want  int
	}{
		{"平和荣和（门清+10）", plain, plainCtx(), 30},
		{"平和自摸（固定 20）", plain, ctxWith(func(c *ScoreContext) { c.Tsumo = true }), 20},
		{"嵌张荣和（+2 待ち，切上 40）", kanchan, plainCtx(), 40},
		{"七对子固定 25", func(t *testing.T) WinShape {
			shape, ok := Decompose(mustParseHand(t, "11m 22m 33m 44p 55p 66s 11z"), nil, tileFromName(t, "1z"))
			if !ok {
				t.Fatal("七对子应判和")
			}
			return shape
		}, plainCtx(), 25},
		{"国士按 25 展示", func(t *testing.T) WinShape {
			shape, ok := Decompose(mustParseHand(t, "19m 19p 19s 1234567z 1m"), nil, tileFromName(t, "1m"))
			if !ok {
				t.Fatal("国士应判和")
			}
			return shape
		}, plainCtx(), 25},
		{"暗刻/暗刻/幺九/单骑", func(t *testing.T) WinShape {
			shape, ok := Decompose(mustParseHand(t, "111m 555p 123s 789s 99s"), nil, tileFromName(t, "9s"))
			if !ok {
				t.Fatal("手牌应判和")
			}
			return shape
		}, plainCtx(), 50},
		{"明刻+单骑（副露）", func(t *testing.T) WinShape {
			return buildShape(t, PatternStandard, "9s", WaitTanki,
				"pon:1z", "chi_closed:234m", "chi_closed:567p", "chi_closed:789s")
		}, ctxWith(func(c *ScoreContext) { c.Menzen = false }), 30},
		{"大明杠（+16 再切上）", func(t *testing.T) WinShape {
			return buildShape(t, PatternStandard, "9s", WaitTanki,
				"kan_daiminkan:1z", "chi_closed:234m", "chi_closed:567p", "chi_closed:789s")
		}, ctxWith(func(c *ScoreContext) { c.Menzen = false }), 40},
		{"暗杠（+32）", func(t *testing.T) WinShape {
			return buildShape(t, PatternStandard, "9s", WaitTanki,
				"kan_ankan:1z", "chi_closed:234m", "chi_closed:567p", "chi_closed:789s")
		}, plainCtx(), 70},
		{"连风雀头 +4", func(t *testing.T) WinShape {
			return buildShape(t, PatternStandard, "1z", WaitRyanmen,
				"chi_closed:123m", "chi_closed:456m", "chi_closed:789m", "chi_closed:456s")
		}, ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.East
			c.RoundWind = tile.East
		}), 40},
		{"喰い平和形荣和 30 符", func(t *testing.T) WinShape {
			return buildShape(t, PatternStandard, "5s", WaitRyanmen,
				"chi:123m", "chi_closed:456m", "chi_closed:789p", "chi_closed:123s")
		}, ctxWith(func(c *ScoreContext) { c.Menzen = false }), 30},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape := tc.shape(t)
			fu, err := Fu(shape, tc.ctx)
			if err != nil {
				t.Fatalf("Fu 返回错误：%v", err)
			}
			if fu != tc.want {
				t.Fatalf("Fu = %d，期望 %d", fu, tc.want)
			}
			// 与 Evaluate 返回的 fu 保持一致。
			if _, _, evalFu, err := Evaluate(shape, tc.ctx); err != nil {
				t.Fatalf("Evaluate 返回错误：%v", err)
			} else if evalFu != fu {
				t.Fatalf("Evaluate 的 fu = %d，与 Fu 的 %d 不一致", evalFu, fu)
			}
		})
	}
}

// TestFuAndEvaluateInvalidInput 覆盖输入校验：非法 shape / 矛盾 ctx 必须返回哨兵错误。
func TestFuAndEvaluateInvalidInput(t *testing.T) {
	validShape := func(t *testing.T) WinShape {
		t.Helper()
		shape, ok := Decompose(mustParseHand(t, plainHand), nil, tileFromName(t, "6s"))
		if !ok {
			t.Fatal("plainHand 应判和")
		}
		return shape
	}

	invalidShape := validShape(t)
	invalidShape.Pattern = "unknown"

	missingMelds := validShape(t)
	missingMelds.Melds = missingMelds.Melds[:3]

	badTile := validShape(t)
	badTile.Jantou = tile.Tile{Kind: tile.NumKinds}

	cases := []struct {
		name  string
		shape WinShape
		ctx   ScoreContext
		want  error
	}{
		{"未知牌型", invalidShape, plainCtx(), ErrInvalidShape},
		{"标准型面子不足", missingMelds, plainCtx(), ErrInvalidShape},
		{"将牌越界", badTile, plainCtx(), ErrInvalidShape},
		{"明暗与 ctx 不一致", validShape(t), ctxWith(func(c *ScoreContext) { c.Menzen = false }), ErrInvalidContext},
		{"一发自摸却未立直", validShape(t), ctxWith(func(c *ScoreContext) { c.Ippatsu = true }), ErrInvalidContext},
		{"海底却荣和", validShape(t), ctxWith(func(c *ScoreContext) { c.Haitei = true }), ErrInvalidContext},
		{"自风非法", validShape(t), ScoreContext{Menzen: true, SeatWind: tile.Man1, RoundWind: tile.East}, ErrInvalidContext},
		{"天和却不是亲家", validShape(t), ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.South
			c.Tsumo = true
			c.Tenhou = true
		}), ErrInvalidContext},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Fu(tc.shape, tc.ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Fu 返回 %v，期望 %v", err, tc.want)
			}
			if _, _, _, err := Evaluate(tc.shape, tc.ctx); !errors.Is(err, tc.want) {
				t.Fatalf("Evaluate 返回 %v，期望 %v", err, tc.want)
			}
		})
	}
}

// TestScore 覆盖点数分配的固定表：不同翻/符、亲/子、荣和/自摸、满贯阶梯。
func TestScore(t *testing.T) {
	child := ctxWith(func(c *ScoreContext) { c.SeatWind = tile.South })
	dealer := ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East })
	childTsumo := ctxWith(func(c *ScoreContext) { c.SeatWind = tile.South; c.Tsumo = true })
	dealerTsumo := ctxWith(func(c *ScoreContext) { c.SeatWind = tile.East; c.Tsumo = true })

	cases := []struct {
		name                          string
		han, fu                       int
		ctx                           ScoreContext
		wantRon                       int
		wantTsumoChild, wantTsumoDeal int
		wantTotal                     int
	}{
		{"30符2翻 子荣和", 2, 30, child, 2000, 0, 0, 2000},
		{"30符2翻 亲荣和", 2, 30, dealer, 2900, 0, 0, 2900},
		{"25符2翻 子荣和（七对）", 2, 25, child, 1600, 0, 0, 1600},
		{"25符2翻 亲荣和（七对）", 2, 25, dealer, 2400, 0, 0, 2400},
		{"25符3翻 子荣和", 3, 25, child, 3200, 0, 0, 3200},
		{"30符3翻 子荣和", 3, 30, child, 3900, 0, 0, 3900},
		{"30符3翻 亲荣和", 3, 30, dealer, 5800, 0, 0, 5800},
		{"30符4翻 子荣和（7700，未满贯）", 4, 30, child, 7700, 0, 0, 7700},
		{"40符4翻 子荣和（满贯）", 4, 40, child, 8000, 0, 0, 8000},
		{"70符3翻 子荣和（满贯）", 3, 70, child, 8000, 0, 0, 8000},
		{"20符2翻 子自摸", 2, 20, childTsumo, 0, 400, 700, 1500},
		{"20符2翻 亲自摸", 2, 20, dealerTsumo, 0, 700, 700, 2100},
		{"20符3翻 子自摸", 3, 20, childTsumo, 0, 700, 1300, 2700},
		{"20符4翻 子自摸", 4, 20, childTsumo, 0, 1300, 2600, 5200},
		{"满贯 子荣和", 5, 30, child, 8000, 0, 0, 8000},
		{"满贯 亲荣和", 5, 30, dealer, 12000, 0, 0, 12000},
		{"满贯 子自摸", 5, 30, childTsumo, 0, 2000, 4000, 8000},
		{"满贯 亲自摸", 5, 30, dealerTsumo, 0, 4000, 4000, 12000},
		{"跳满 子自摸", 6, 30, childTsumo, 0, 3000, 6000, 12000},
		{"倍满 子荣和", 8, 30, child, 16000, 0, 0, 16000},
		{"三倍满 子荣和", 11, 30, child, 24000, 0, 0, 24000},
		{"役满 子荣和", 13, 30, child, 32000, 0, 0, 32000},
		{"役满 亲荣和", 13, 30, dealer, 48000, 0, 0, 48000},
		{"役满 亲自摸", 13, 30, dealerTsumo, 0, 16000, 16000, 48000},
		{"双倍役满 子荣和", 26, 30, child, 64000, 0, 0, 64000},
		{"满贯 子荣和 + 2 本场 + 1 立直棒", 5, 30, ctxWith(func(c *ScoreContext) {
			c.Honba = 2
			c.RiichiSticks = 1
		}), 9600, 0, 0, 9600},
		{"满贯 子自摸 + 1 本场", 5, 30, ctxWith(func(c *ScoreContext) {
			c.SeatWind = tile.South
			c.Tsumo = true
			c.Honba = 1
		}), 0, 2000, 4000, 8300},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pts := Score(tc.han, tc.fu, tc.ctx)
			if pts.Han != tc.han || pts.Fu != tc.fu {
				t.Fatalf("Han/Fu 回填错误：%d/%d", pts.Han, pts.Fu)
			}
			if pts.Ron != tc.wantRon {
				t.Fatalf("Ron = %d，期望 %d", pts.Ron, tc.wantRon)
			}
			if pts.TsumoChild != tc.wantTsumoChild || pts.TsumoDealer != tc.wantTsumoDeal {
				t.Fatalf("自摸支付 = %d/%d，期望 %d/%d",
					pts.TsumoChild, pts.TsumoDealer, tc.wantTsumoChild, tc.wantTsumoDeal)
			}
			if pts.Total != tc.wantTotal {
				t.Fatalf("Total = %d，期望 %d", pts.Total, tc.wantTotal)
			}
			// 亲家自摸时 TsumoChild 与 TsumoDealer 相同。
			if tc.ctx.Tsumo && tc.ctx.SeatWind == tile.East && pts.TsumoChild != pts.TsumoDealer {
				t.Fatalf("亲家自摸的支付应一致：%d/%d", pts.TsumoChild, pts.TsumoDealer)
			}
		})
	}

	if pts := Score(0, 30, child); pts.Total != 0 || pts.Ron != 0 {
		t.Fatalf("0 翻不应得分：%+v", pts)
	}
}

// TestCountDora 覆盖宝牌指示牌 → 宝牌的循环规则与赤宝牌统计。
func TestCountDora(t *testing.T) {
	// 指示牌 → 宝牌：9m→1m、5p→6p、北(4z)→东(1z)、中(7z)→白(5z)。
	cycleCases := []struct {
		indicator string
		doraIn    string
	}{
		{"9m", "1m 1m"},
		{"5p", "6p 6p 6p"},
		{"4z", "1z"},
		{"7z", "5z"},
	}
	for _, tc := range cycleCases {
		t.Run("指示牌:"+tc.indicator, func(t *testing.T) {
			tiles := mustParseHand(t, tc.doraIn)
			ctx := plainCtx()
			ctx.DoraIndicators = []tile.Tile{tileFromName(t, tc.indicator)}
			if got := CountDoraFromTiles(tiles, ctx); got != len(tiles) {
				t.Fatalf("宝牌数 = %d，期望 %d", got, len(tiles))
			}
		})
	}

	// 指示牌不在手里 → 0 张；非法指示牌不 panic。
	ctx := plainCtx()
	ctx.DoraIndicators = []tile.Tile{tileFromName(t, "9m"), {Kind: tile.NumKinds}}
	tiles := mustParseHand(t, "2p 3p")
	if got := CountDoraFromTiles(tiles, ctx); got != 0 {
		t.Fatalf("宝牌数 = %d，期望 0", got)
	}

	// 赤宝牌：shape 里的 Aka 标记计入 CountAka / CountDora。
	red5s := tileFromName(t, "r5s")
	hand := mustParseHand(t, plainHand)
	for i := range hand {
		if hand[i].Kind == tile.Sou5 {
			hand[i] = red5s
		}
	}
	shape, ok := Decompose(hand, nil, tileFromName(t, "6s"))
	if !ok {
		t.Fatal("带赤 5s 的手牌应判和")
	}
	if got := CountAka(shape); got != 1 {
		t.Fatalf("CountAka = %d，期望 1", got)
	}
	akaCtx := plainCtx()
	akaCtx.AkaDora = CountAka(shape)
	if got := CountDora(shape, akaCtx); got != 1 {
		t.Fatalf("CountDora = %d，期望 1（赤 1 张）", got)
	}
}

// TestSevenPairsStrictAffectsFu 断言严格七对语义下 4 张相同不会判七对（也就不会有 25 符特例）。
func TestSevenPairsStrictAffectsFu(t *testing.T) {
	hand := mustParseHand(t, "1111m 22p 33p 44s 55s 11z")
	if shape, ok := Decompose(hand, nil, hand[0]); ok {
		t.Fatalf("严格模式不应判七对：%+v", shape)
	}
	shape, ok := DecomposeWithOptions(hand, nil, hand[0], CompatOptions())
	if !ok {
		t.Fatal("v1 兼容模式应判七对")
	}
	fu, err := Fu(shape, plainCtx())
	if err != nil {
		t.Fatalf("Fu 返回错误：%v", err)
	}
	if fu != 25 {
		t.Fatalf("七对符 = %d，期望 25", fu)
	}
}
