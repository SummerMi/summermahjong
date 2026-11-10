package rules

import (
	"testing"

	"summermahjong/riichi/internal/tile"
)

// meldWinCase 是一条「副露后和牌」用例：11 张门清手牌 + 1 组副露 = 14 张和牌形。
//
// S3 第一版只支持门清（Decompose 对 melds 非空返回 false），
// 这些用例现在用来锁定「暂不支持」的行为；S5 实现副露拆解后把 t.Skip 去掉即可。
type meldWinCase struct {
	name     string
	hand     string // 门清部分：3 组面子 + 将（11 张）
	meldKind string
	meld     []string // 副露的牌（碰/吃 3 张，杠 4 张）
	winning  string
}

// meldWinCases 覆盖碰/吃/杠、数牌/字牌、多种将牌的组合，共 12 条。
var meldWinCases = []meldWinCase{
	{"碰 1p + 三组顺子 + 9s 将", "123m 456m 789m 99s", MeldPon, []string{"1p", "1p", "1p"}, "9s"},
	{"碰 2s + 单骑 1p", "123m 456m 789m 11p", MeldPon, []string{"2s", "2s", "2s"}, "1p"},
	{"碰东 + 单骑 1p", "123m 456m 789m 11p", MeldPon, []string{"1z", "1z", "1z"}, "1p"},
	{"碰白 + 单骑 1s", "123m 456m 789m 11s", MeldPon, []string{"5z", "5z", "5z"}, "1s"},
	{"吃 123p + 9s 将", "456m 789m 123s 99s", MeldChi, []string{"1p", "2p", "3p"}, "9s"},
	{"吃 789s + 1p 将", "123m 456m 11p 999s", MeldChi, []string{"7s", "8s", "9s"}, "1p"},
	{"吃 456s + 9m 将", "123m 456m 11p 999m", MeldChi, []string{"4s", "5s", "6s"}, "9m"},
	{"碰 9m + 1p 将", "123p 456p 789p 11p", MeldPon, []string{"9m", "9m", "9m"}, "1p"},
	{"碰中 + 9p 将", "123m 456m 789m 99p", MeldPon, []string{"7z", "7z", "7z"}, "9p"},
	{"吃 345m + 9s 将", "11p 567m 789s 999s", MeldChi, []string{"3m", "4m", "5m"}, "9s"},
	{"暗杠 1m + 99p 将", "234m 345p 678s 99p", MeldKanAnkan, []string{"1m", "1m", "1m", "1m"}, "9p"},
	{"碰 3s + 11m 将", "123m 456m 789m 11m", MeldPon, []string{"3s", "3s", "3s"}, "1m"},
}

// buildMeldWinCase 把用例描述转换成 Decompose 的入参。
func buildMeldWinCase(t *testing.T, tc meldWinCase) (hand []tile.Tile, melds []Meld, winning tile.Tile) {
	t.Helper()
	hand = mustParseHand(t, tc.hand)
	melds = []Meld{{Kind: tc.meldKind, Tiles: parseTileNames(t, tc.meld)}}
	winning = tileFromName(t, tc.winning)
	return hand, melds, winning
}

// TestMeldWinCasesUnsupportedNow 锁定 S3 的已知限制：副露后和牌暂不支持，必须返回 false，
// 而不是错误地当成「不和牌」或 panic。S5 实现后这个测试会被删除，改由下面的 t.Skip 用例接管。
func TestMeldWinCasesUnsupportedNow(t *testing.T) {
	if len(meldWinCases) < 10 {
		t.Fatalf("副露和牌用例只有 %d 条，要求至少 10 条", len(meldWinCases))
	}
	for _, tc := range meldWinCases {
		t.Run(tc.name, func(t *testing.T) {
			hand, melds, winning := buildMeldWinCase(t, tc)
			if shape, ok := Decompose(hand, melds, winning); ok {
				t.Fatalf("S3 未实现副露拆解，不应返回成功：%+v", shape)
			}
			if IsWinning(hand, melds, winning) {
				t.Fatal("S3 未实现副露拆解，IsWinning 应返回 false")
			}
		})
	}
}

// TestMeldWinCasesFuture 是 S5 的验收清单：副露实现后应全部判和（standard），
// 且拆解出的面子数仍为 4（副露占其中若干组）。
//
// 现在标记 t.Skip：S3 第一版只支持门清，等 S5 实现副露拆解后去掉 Skip，
// 并把本文件顶部的说明改成「已支持」。
func TestMeldWinCasesFuture(t *testing.T) {
	t.Skip("S3 第一版只支持门清；副露拆解由 S5 实现，届时去掉本 Skip 并断言 true")
	for _, tc := range meldWinCases {
		t.Run(tc.name, func(t *testing.T) {
			hand, melds, winning := buildMeldWinCase(t, tc)
			shape, ok := Decompose(hand, melds, winning)
			if !ok {
				t.Fatalf("S5 实现副露后应判和：hand=%q melds=%+v", tc.hand, melds)
			}
			if shape.Pattern != PatternStandard {
				t.Fatalf("Pattern = %q，期望 %q", shape.Pattern, PatternStandard)
			}
			if len(shape.Melds) != meldsRequired {
				t.Fatalf("面子数 = %d，期望 %d（含副露）", len(shape.Melds), meldsRequired)
			}
		})
	}
}

// TestMeldWinCasesAreValidHands 自检用例数据本身合法：门清 11 张、副露是合法面子、
// 合起来每种牌不超过 4 张（杠多一张，物理张数 15 与契约一致）。
func TestMeldWinCasesAreValidHands(t *testing.T) {
	for _, tc := range meldWinCases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			if len(hand) != 11 {
				t.Fatalf("门清部分应为 11 张，实际 %d 张", len(hand))
			}
			meldTiles := parseTileNames(t, tc.meld)

			switch tc.meldKind {
			case MeldChi:
				if len(meldTiles) != 3 {
					t.Fatalf("吃应为 3 张，实际 %d 张", len(meldTiles))
				}
				a, b, c := meldTiles[0].Kind, meldTiles[1].Kind, meldTiles[2].Kind
				if a.Suit() == tile.Honor || a.Suit() != b.Suit() || b.Suit() != c.Suit() ||
					b != a+1 || c != b+1 {
					t.Fatalf("吃必须是同花色连续三张，实际 %v", meldTiles)
				}
			case MeldPon, MeldKanDaiminkan, MeldKanShouminkan, MeldKanAnkan:
				wantLen := 3
				if tc.meldKind != MeldPon {
					wantLen = 4
				}
				if len(meldTiles) != wantLen {
					t.Fatalf("%s 应为 %d 张，实际 %d 张", tc.meldKind, wantLen, len(meldTiles))
				}
				for _, mt := range meldTiles[1:] {
					if mt.Kind != meldTiles[0].Kind {
						t.Fatalf("%s 的牌必须相同，实际 %v", tc.meldKind, meldTiles)
					}
				}
			default:
				t.Fatalf("未知副露类型 %q", tc.meldKind)
			}

			// 副露 + 手牌合在一起，每种牌不超过 4 张。
			all := append(cloneTiles(hand), meldTiles...)
			for kind, n := range kindCounts(all) {
				if n > tile.CopiesPerKind {
					t.Fatalf("牌 %v 共出现 %d 张，超过 4 张", tile.Kind(kind), n)
				}
			}
		})
	}
}
