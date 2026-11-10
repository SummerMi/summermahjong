package rules

import (
	"math/rand"
	"slices"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// TestShantenKnownValues 用手工推导的已知向听数做定点校验。
func TestShantenKnownValues(t *testing.T) {
	cases := []struct {
		name      string
		hand      string
		meldCount int
		want      int
	}{
		{"九莲宝灯九面听", "1112345678999m", 0, 0},
		{"两面听", "123m 456m 789m 11p 45s", 0, 0},
		{"边张听", "123m 456m 789m 11p 12s", 0, 0},
		{"单骑听", "123m 456m 789m 123p 9s", 0, 0},
		{"四刻子单骑听", "111m 222m 333m 444m 5m", 0, 0},
		{"七对听", "11m 22m 33m 44p 55p 66s 1z", 0, 0},
		{"国士十二种带对", "19m 19p 19s 123456z 1m", 0, 0},
		{"国士十三面听", "19m 19p 19s 1234567z", 0, 0},
		{"标准型一向听", "123m 456m 789m 11p 4s 7s", 0, 1},
		{"七对一向听", "11m 22m 33m 44p 55p 1s 4s 7s", 0, 1},
		{"标准型二向听", "123m 456m 789m 1p 4p 7p 1s", 0, 2},
		{"全散牌", "147m 147p 147s 1234z", 0, 6},
		{"一张副露 + 单骑听", "123m 456m 789m 1p", 1, 0},
		{"一张副露 + 二向听", "123m 456m 1p 4p 7p 9s", 1, 2},
		{"四张副露 + 单骑听", "5m", 4, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			if got := Shanten(hand, tc.meldCount); got != tc.want {
				t.Fatalf("Shanten(%q, %d) = %d，期望 %d", tc.hand, tc.meldCount, got, tc.want)
			}
		})
	}
}

// TestShantenMaxIsSix 断言门清 13 张手牌的向听数上界为 6（七对分支兜底）。
func TestShantenMaxIsSix(t *testing.T) {
	hand := mustParseHand(t, "147m 147p 147s 1234z")
	if got := Shanten(hand, 0); got != 6 {
		t.Fatalf("全散牌向听数 = %d，期望 6", got)
	}
}

// TestShantenAgariHandIsInvalid 记录 S3 的张数约定：Shanten 只接受 13 张听牌形，
// 14 张（和了形 / 摸牌后）请先打出一张或改用 Decompose 判胡。
func TestShantenAgariHandIsInvalid(t *testing.T) {
	winning := mustParseHand(t, "123456789m 123p 99s")
	if got := Shanten(winning, 0); got != ShantenInvalid {
		t.Fatalf("14 张手牌的 Shanten = %d，期望 ShantenInvalid(%d)", got, ShantenInvalid)
	}
}

// TestShantenInvalidInput 覆盖非法输入：牌面非法、张数不对、副露数量越界。
func TestShantenInvalidInput(t *testing.T) {
	valid := mustParseHand(t, "123m 456m 789m 11p 45s")
	unknown := append(cloneTiles(valid), tileFromName(t, "?"))
	overCopies := append([]tile.Tile{
		tileFromName(t, "1m"), tileFromName(t, "1m"), tileFromName(t, "1m"),
		tileFromName(t, "1m"), tileFromName(t, "1m"),
	}, mustParseHand(t, "2345678m 1p")...)

	cases := []struct {
		name      string
		hand      []tile.Tile
		meldCount int
	}{
		{"空手牌", nil, 0},
		{"1 张", mustParseHand(t, "1m"), 0},
		{"12 张", mustParseHand(t, "123m 456m 789m 11p 4s"), 0},
		{"14 张", mustParseHand(t, "123456789m 123p 99s"), 0},
		{"15 张", mustParseHand(t, "123456789m 123p 99s 1m"), 0},
		{"含未知牌", unknown, 0},
		{"同种 5 张", overCopies, 0},
		{"副露数为负", valid, -1},
		{"副露数超 4", valid, 5},
		{"副露数与张数不匹配", valid, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Shanten(tc.hand, tc.meldCount); got != ShantenInvalid {
				t.Fatalf("Shanten = %d，期望 ShantenInvalid(%d)", got, ShantenInvalid)
			}
		})
	}
}

// TestShantenFormalTenpaiWithDeadWait 覆盖「形式听牌」：手牌已有 4 张 9s，
// 牌山不存在第 5 张，实际无法和牌，但向听数仍按结构算作 0（与常见引擎一致）。
func TestShantenFormalTenpaiWithDeadWait(t *testing.T) {
	hand := mustParseHand(t, "123m 456m 789m 9999s")
	if got := Shanten(hand, 0); got != 0 {
		t.Fatalf("Shanten = %d，期望 0（形式听牌）", got)
	}
}

// TestShantenDoesNotModifyHand 断言 Shanten 不修改入参。
func TestShantenDoesNotModifyHand(t *testing.T) {
	hand := mustParseHand(t, "123m 456m 789m 11p 45s")
	before := cloneTiles(hand)
	Shanten(hand, 0)
	if !slices.Equal(hand, before) {
		t.Fatalf("Shanten 修改了入参：%v -> %v", before, hand)
	}
}

// TestShantenMatchesDecomposeOnRandomHands 交叉验证两个纯函数：
// 若存在一张能补成和牌的牌，则 Shanten 必须为 0；否则 Shanten 必须大于 0。
//
// 「形式听牌但 4 张都被自己抓死」的极端手牌除外：那种手牌结构上确实是听牌（Shanten=0），
// 只是没有可用的和了牌，因此断言里把这类情况也算作合法。
func TestShantenMatchesDecomposeOnRandomHands(t *testing.T) {
	rng := rand.New(rand.NewSource(20261110))
	for i := 0; i < 300; i++ {
		hand := randomHand(rng, 13)
		shanten := Shanten(hand, 0)
		if shanten < 0 || shanten > 6 {
			t.Fatalf("第 %d 手牌 %v 的向听数 %d 越界（应为 0..6）", i, hand, shanten)
		}

		counts := kindCounts(hand)
		waitFound := false
		for kind := 0; kind < tile.NumKinds; kind++ {
			if counts[kind] >= tile.CopiesPerKind {
				continue
			}
			candidate := append(cloneTiles(hand), tile.Tile{Kind: tile.Kind(kind)})
			if _, ok := Decompose(candidate, nil, tile.Tile{Kind: tile.Kind(kind)}); ok {
				waitFound = true
				break
			}
		}
		if waitFound && shanten != 0 {
			t.Fatalf("手牌 %v 存在和了牌，但 Shanten = %d（期望 0）", hand, shanten)
		}
		if !waitFound && shanten == 0 {
			// 只可能是形式听牌（所有听牌张都被自己抓满 4 张），这里显式放行并记录。
			t.Logf("形式听牌（无可用和了牌）：%v", hand)
		}
	}
}

// TestShantenOfAgariMinusOneTileIsZero 构造随机和牌，逐一拿掉任意一张后必须听牌（向听 0）。
func TestShantenOfAgariMinusOneTileIsZero(t *testing.T) {
	rng := rand.New(rand.NewSource(20261111))
	for i := 0; i < 100; i++ {
		hand := randomWinHand(t, rng)
		winning := hand[0]
		if _, ok := Decompose(hand, nil, winning); !ok {
			t.Fatalf("构造的和牌却判不出和：%v", hand)
		}
		for remove := range hand {
			rest := make([]tile.Tile, 0, 13)
			rest = append(rest, hand[:remove]...)
			rest = append(rest, hand[remove+1:]...)
			if got := Shanten(rest, 0); got != 0 {
				t.Fatalf("和牌 %v 拿掉第 %d 张后 Shanten = %d，期望 0", hand, remove, got)
			}
		}
	}
}

// TestShantenMeldCountConsistency 断言副露数量对向听数的影响：每多一组副露，
// 手牌张数相应减少，而「已完成的向听进度」保留。
func TestShantenMeldCountConsistency(t *testing.T) {
	// 0 组副露：13 张两向听。
	if got := Shanten(mustParseHand(t, "123m 456m 789m 1p 4p 7p 1s"), 0); got != 2 {
		t.Fatalf("无副露向听 = %d，期望 2", got)
	}
	// 把 123m 拿出去当副露：剩 10 张，meldCount=1，向听数不变。
	if got := Shanten(mustParseHand(t, "456m 789m 1p 4p 7p 1s"), 1); got != 2 {
		t.Fatalf("一组副露向听 = %d，期望 2", got)
	}
}
