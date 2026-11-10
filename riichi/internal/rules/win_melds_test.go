package rules

import (
	"slices"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// meldWinCase 是一条「副露后和牌」用例：暗牌 + 副露 = 14 张和牌形（带杠时物理 14+kans 张）。
//
// S5b 起 Decompose 真正支持 melds：暗牌张数 = 14 - 3*len(melds)，
// 返回的 WinShape.Melds 保留副露并带 Open 标记（吃/碰/明杠 true，暗杠 false）。
type meldWinCase struct {
	name     string
	hand     string // 暗牌部分（含和了牌）
	meldKind string
	meld     []string // 副露的牌（吃/碰 3 张，杠 4 张）
	winning  string
	wantWait WaitType
}

// meldWinCases 覆盖碰/吃/明杠/暗杠，共 16 条（要求 ≥15，且每类 ≥2）。
var meldWinCases = []meldWinCase{
	{"碰 1p + 三组顺子 + 9s 单骑", "123m 456m 789m 99s", MeldPon, []string{"1p", "1p", "1p"}, "9s", WaitTanki},
	{"碰 2s + 单骑 1p", "123m 456m 789m 11p", MeldPon, []string{"2s", "2s", "2s"}, "1p", WaitTanki},
	{"碰东 + 单骑 1p", "123m 456m 789m 11p", MeldPon, []string{"1z", "1z", "1z"}, "1p", WaitTanki},
	{"碰白 + 单骑 1s", "123m 456m 789m 11s", MeldPon, []string{"5z", "5z", "5z"}, "1s", WaitTanki},
	{"吃 123p + 9s 单骑", "456m 789m 123s 99s", MeldChi, []string{"1p", "2p", "3p"}, "9s", WaitTanki},
	{"吃 789s + 1p 单骑", "123m 456m 11p 999s", MeldChi, []string{"7s", "8s", "9s"}, "1p", WaitTanki},
	{"吃 456s + 9m 将", "123m 456m 11p 999m", MeldChi, []string{"4s", "5s", "6s"}, "9m", WaitShanpon},
	{"碰 9m + 1p 将", "123p 456p 789p 11p", MeldPon, []string{"9m", "9m", "9m"}, "1p", WaitRyanmen},
	{"碰中 + 9p 单骑", "123m 456m 789m 99p", MeldPon, []string{"7z", "7z", "7z"}, "9p", WaitTanki},
	{"吃 345m + 9s 将", "11p 567m 789s 999s", MeldChi, []string{"3m", "4m", "5m"}, "9s", WaitRyanmen},
	{"暗杠 1m + 99p 单骑", "234m 345p 678s 99p", MeldKanAnkan, []string{"1m", "1m", "1m", "1m"}, "9p", WaitTanki},
	{"碰 3s + 11m 将", "123m 456m 789m 11m", MeldPon, []string{"3s", "3s", "3s"}, "1m", WaitRyanmen},
	{"大明杠 东 + 99p 单骑", "234m 345p 678s 99p", MeldKanDaiminkan, []string{"1z", "1z", "1z", "1z"}, "9p", WaitTanki},
	{"大明杠 9s + 11s 单骑", "123m 456p 789m 11s", MeldKanDaiminkan, []string{"9s", "9s", "9s", "9s"}, "1s", WaitTanki},
	{"暗杠 白 + 11m 将", "123m 456m 789p 11m", MeldKanAnkan, []string{"5z", "5z", "5z", "5z"}, "1m", WaitRyanmen},
	{"加杠 3p + 99m 单骑", "123m 456m 789s 99m", MeldKanShouminkan, []string{"3p", "3p", "3p", "3p"}, "9m", WaitTanki},
}

// TestDecomposeFourMelds 覆盖 4 组副露：暗牌只剩将牌 2 张，待ち只能是单骑。
func TestDecomposeFourMelds(t *testing.T) {
	hand := mustParseHand(t, "99m")
	melds := []Meld{
		calledMeld(t, MeldPon, "1z", "1z", "1z"),
		calledMeld(t, MeldPon, "5z", "5z", "5z"),
		calledMeld(t, MeldChi, "1p", "2p", "3p"),
		calledMeld(t, MeldChi, "4s", "5s", "6s"),
	}
	winning := tileFromName(t, "9m")
	shape, ok := Decompose(hand, melds, winning)
	if !ok {
		t.Fatal("四副露 + 将牌 2 张应判和")
	}
	if len(shape.Melds) != meldsRequired {
		t.Fatalf("面子数 = %d，期望 4", len(shape.Melds))
	}
	if shape.Wait != WaitTanki {
		t.Fatalf("Wait = %q，期望 tanki", shape.Wait)
	}
	if shape.Jantou.Kind != tile.Man9 {
		t.Fatalf("将牌 = %v，期望 9m", shape.Jantou)
	}
	verifyShapeWithMelds(t, hand, melds, shape, winning)
}

// buildMeldWinCase 把用例描述转换成 Decompose 的入参。
func buildMeldWinCase(t *testing.T, tc meldWinCase) ([]tile.Tile, []Meld, tile.Tile) {
	t.Helper()
	hand := mustParseHand(t, tc.hand)
	melds := []Meld{{Kind: tc.meldKind, Tiles: parseTileNames(t, tc.meld)}}
	winning := tileFromName(t, tc.winning)
	return hand, melds, winning
}

// TestMeldWinCases 逐条断言副露和牌能被正确拆解，且副露的 Kind/Tiles/Open 完整保留。
func TestMeldWinCases(t *testing.T) {
	if len(meldWinCases) < 15 {
		t.Fatalf("副露和牌用例只有 %d 条，要求至少 15 条", len(meldWinCases))
	}
	for _, tc := range meldWinCases {
		t.Run(tc.name, func(t *testing.T) {
			hand, melds, winning := buildMeldWinCase(t, tc)
			shape, ok := Decompose(hand, melds, winning)
			if !ok {
				t.Fatalf("副露和牌应判和：hand=%q melds=%+v", tc.hand, melds)
			}
			if shape.Pattern != PatternStandard {
				t.Fatalf("Pattern = %q，期望 %q", shape.Pattern, PatternStandard)
			}
			if len(shape.Melds) != meldsRequired {
				t.Fatalf("面子数 = %d，期望 %d（含副露）", len(shape.Melds), meldsRequired)
			}
			if shape.Wait != tc.wantWait {
				t.Fatalf("Wait = %q，期望 %q", shape.Wait, tc.wantWait)
			}
			verifyShapeWithMelds(t, hand, melds, shape, winning)
		})
	}
}

// TestMeldOpenFlags 断言 Open 标记：吃/碰/明杠为 true，暗杠与暗牌拆出的面子为 false。
func TestMeldOpenFlags(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		tiles []string
		want  bool
	}{
		{"吃是明面", MeldChi, []string{"1p", "2p", "3p"}, true},
		{"碰是明面", MeldPon, []string{"5z", "5z", "5z"}, true},
		{"大明杠是明面", MeldKanDaiminkan, []string{"9s", "9s", "9s", "9s"}, true},
		{"加杠是明面", MeldKanShouminkan, []string{"9s", "9s", "9s", "9s"}, true},
		{"暗杠是暗面", MeldKanAnkan, []string{"9s", "9s", "9s", "9s"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closed := mustParseHand(t, "123m 456m 789m 11p")
			melds := []Meld{{Kind: tc.kind, Tiles: parseTileNames(t, tc.tiles)}}
			shape, ok := Decompose(closed, melds, tileFromName(t, "1p"))
			if !ok {
				t.Fatal("应判和")
			}
			var called *MeldShape
			for i := range shape.Melds {
				if shape.Melds[i].Kind == tc.kind {
					called = &shape.Melds[i]
				}
			}
			if called == nil {
				t.Fatalf("拆解结果里找不到副露 %q：%+v", tc.kind, shape.Melds)
			}
			if called.Open != tc.want {
				t.Fatalf("%s 的 Open = %v，期望 %v", tc.kind, called.Open, tc.want)
			}
			// 暗牌拆出来的面子必须全是 Open=false。
			for _, meld := range shape.Melds {
				if meld.Kind == tc.kind && len(meld.Tiles) == len(called.Tiles) {
					continue
				}
				if meld.Open {
					t.Fatalf("暗牌拆出的面子不应是 Open：%+v", meld)
				}
			}
		})
	}
}

// TestDecomposeMeldInvalidInput 覆盖副露结构非法时 Decompose 返回 false（不能 panic / 误判）。
func TestDecomposeMeldInvalidInput(t *testing.T) {
	closed := mustParseHand(t, "123m 456m 789m 11p") // 11 张，配 1 组副露刚好
	winning := tileFromName(t, "1p")

	meld := func(kind string, tiles ...string) []Meld {
		return []Meld{{Kind: kind, Tiles: parseTileNames(t, tiles)}}
	}

	cases := []struct {
		name  string
		hand  []tile.Tile
		melds []Meld
	}{
		{"暗牌张数不匹配（14 张 + 1 副露）", mustParseHand(t, plainHand), meld(MeldPon, "5z", "5z", "5z")},
		{"副露数量超过 4", mustParseHand(t, "11p"), []Meld{
			{Kind: MeldPon, Tiles: parseTileNames(t, []string{"5z", "5z", "5z"})},
			{Kind: MeldPon, Tiles: parseTileNames(t, []string{"6z", "6z", "6z"})},
			{Kind: MeldPon, Tiles: parseTileNames(t, []string{"7z", "7z", "7z"})},
			{Kind: MeldPon, Tiles: parseTileNames(t, []string{"1z", "1z", "1z"})},
			{Kind: MeldPon, Tiles: parseTileNames(t, []string{"2z", "2z", "2z"})},
		}},
		{"未知副露类型", closed, meld("foo", "5z", "5z", "5z")},
		{"吃不是顺子", closed, meld(MeldChi, "1p", "2p", "4p")},
		{"吃跨花色", closed, meld(MeldChi, "1m", "2p", "3p")},
		{"碰的牌不同", closed, meld(MeldPon, "5z", "5z", "6z")},
		{"碰只有 2 张", closed, meld(MeldPon, "5z", "5z")},
		{"杠只有 3 张", closed, meld(MeldKanDaiminkan, "5z", "5z", "5z")},
		{"和了牌不在暗牌里", closed, meld(MeldPon, "5z", "5z", "5z")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := winning
			if tc.name == "和了牌不在暗牌里" {
				w = tileFromName(t, "1z")
			}
			if shape, ok := Decompose(tc.hand, tc.melds, w); ok {
				t.Fatalf("非法输入应返回 false，实际拆解出 %+v", shape)
			}
		})
	}
}

// TestMeldWinCasesAreValidHands 自检用例数据本身合法：暗牌 11 张、副露是合法面子、
// 合起来每种牌不超过 4 张（杠多一张，物理张数 15 与契约一致）。
func TestMeldWinCasesAreValidHands(t *testing.T) {
	for _, tc := range meldWinCases {
		t.Run(tc.name, func(t *testing.T) {
			hand := mustParseHand(t, tc.hand)
			if len(hand) != 11 {
				t.Fatalf("暗牌应为 11 张，实际 %d 张", len(hand))
			}
			meldTiles := parseTileNames(t, tc.meld)

			switch tc.meldKind {
			case MeldChi:
				if len(meldTiles) != 3 || !isChiTiles(meldTiles) {
					t.Fatalf("吃必须是同花色连续三张，实际 %v", meldTiles)
				}
			case MeldPon, MeldKanDaiminkan, MeldKanShouminkan, MeldKanAnkan:
				wantLen := 3
				if tc.meldKind != MeldPon {
					wantLen = 4
				}
				if len(meldTiles) != wantLen || !isPonTiles(meldTiles) {
					t.Fatalf("%s 应为 %d 张同种牌，实际 %v", tc.meldKind, wantLen, meldTiles)
				}
			default:
				t.Fatalf("未知副露类型 %q", tc.meldKind)
			}

			// 副露 + 暗牌合在一起，每种牌不超过 4 张。
			all := append(slices.Clone(hand), meldTiles...)
			for kind, n := range kindCounts(all) {
				if n > tile.CopiesPerKind {
					t.Fatalf("牌 %v 共出现 %d 张，超过 4 张", tile.Kind(kind), n)
				}
			}
		})
	}
}
