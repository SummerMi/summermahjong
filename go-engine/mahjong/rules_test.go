package mahjong

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 本文件覆盖两类验证：
//
//  1. 结构性单元测试：牌解析、牌墙、洗牌、发牌、排序计数、非法输入；
//  2. 语义等价性测试：直接读 go-engine/testdata/ 下的 fixture。
//     fixture 由 go-engine/tools/gen_fixtures.py 调用 Python 版
//     app/services/mahjong/rules.py 真实生成，期望值不是手写的；
//     包括 39 条精选用例（胡牌 / 七对 / 未胡 / 听牌 / 非法输入）与
//     2000 条随机手牌（含构造出来的胡牌与听牌正样本）。

// fixtureCase 对应 Python 生成器写出的单条用例。
type fixtureCase struct {
	ID      string   `json:"id"`
	Check   string   `json:"check"` // "win"（14 张）或 "wait"（13 张）
	Hand    string   `json:"hand"`  // 简写文本，用于顺带验证 ParseHand
	Tiles   []string `json:"tiles"` // 展开后的牌面
	Size    int      `json:"size"`
	Kind    string   `json:"kind"`
	Pattern *string  `json:"pattern"` // 14 张用例：null 表示未胡
	IsWin   *bool    `json:"is_win"`
	Waits   []string `json:"waits"` // 13 张用例：期望听牌
	Error   string   `json:"error"` // 非空表示期望 InvalidHandError
}

// fixtureFile 是 fixture JSON 的顶层结构。
type fixtureFile struct {
	Meta  fixtureMeta   `json:"meta"`
	Cases []fixtureCase `json:"cases"`
}

// fixtureMeta 只取测试需要的元信息。
type fixtureMeta struct {
	PythonVersion string `json:"python_version"`
	Stats         struct {
		Total int `json:"total"`
	} `json:"stats"`
}

// loadFixture 读取 testdata 下的 fixture 文件。
func loadFixture(t *testing.T, name string) fixtureFile {
	t.Helper()
	path := filepath.Join("..", "testdata", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 fixture 失败（%s）：%v", path, err)
	}
	var fixture fixtureFile
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("解析 fixture 失败（%s）：%v", path, err)
	}
	return fixture
}

// parseFixtureHand 把 fixture 里的牌面字符串转成 Tile 切片。
// allowInvalid 为 true 时，未知牌用越界 Tile 模拟（非法输入用例需要）。
func parseFixtureHand(t *testing.T, names []string, allowInvalid bool) []Tile {
	t.Helper()
	hand := make([]Tile, 0, len(names))
	for _, name := range names {
		tile, err := ParseTile(name)
		if err != nil {
			if !allowInvalid {
				t.Fatalf("fixture 里的牌 %q 无法解析：%v", name, err)
			}
			hand = append(hand, Tile(NumTileKinds))
			continue
		}
		hand = append(hand, tile)
	}
	return hand
}

// wantPattern 把 fixture 的 pattern 字段转成期望的 Pattern。
func wantPattern(tc fixtureCase) Pattern {
	if tc.Pattern == nil {
		return PatternNone
	}
	return Pattern(*tc.Pattern)
}

// requireInvalidHand 断言 err 是手牌非法错误（同时应匹配领域基类 ErrMahjong）。
func requireInvalidHand(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("期望返回 InvalidHandError，实际返回 nil")
	}
	if !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("期望 errors.Is(err, ErrInvalidHand)，实际 err = %v", err)
	}
	if !errors.Is(err, ErrMahjong) {
		t.Fatalf("InvalidHandError 应当同时匹配 ErrMahjong，实际 err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 精选用例：期望值全部来自 Python 引擎
// ---------------------------------------------------------------------------

func TestCuratedFixture(t *testing.T) {
	fixture := loadFixture(t, "cases.json")
	if len(fixture.Cases) == 0 {
		t.Fatal("精选用例为空，请先运行 go-engine/tools/gen_fixtures.py")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			hand := parseFixtureHand(t, tc.Tiles, tc.Error != "")

			// 非法输入：Go 侧必须同样拒绝，且错误可被 errors.Is 识别。
			if tc.Error != "" {
				switch tc.Check {
				case "win":
					_, err := WinningPattern(hand)
					requireInvalidHand(t, err)
					_, err = IsWinningHand(hand)
					requireInvalidHand(t, err)
				case "wait":
					_, err := WaitingTiles(hand)
					requireInvalidHand(t, err)
				default:
					t.Fatalf("未知 check 类型：%q", tc.Check)
				}
				return
			}

			// 顺带验证简写解析：fixture 的 hand 文本解析结果应与 tiles 一致。
			if tc.Hand != "" {
				parsed, err := ParseHand(tc.Hand)
				if err != nil {
					t.Fatalf("ParseHand(%q) 失败：%v", tc.Hand, err)
				}
				if !slices.Equal(parsed, hand) {
					t.Fatalf("ParseHand(%q) = %v，期望 %v", tc.Hand, parsed, hand)
				}
			}

			switch tc.Check {
			case "win":
				if tc.Size != WinHandSize {
					t.Fatalf("14 张用例的 size 字段是 %d", tc.Size)
				}
				pattern, err := WinningPattern(hand)
				if err != nil {
					t.Fatalf("WinningPattern 返回错误：%v", err)
				}
				if want := wantPattern(tc); pattern != want {
					t.Fatalf("WinningPattern = %q，Python 期望 %q", pattern, want)
				}
				isWin, err := IsWinningHand(hand)
				if err != nil {
					t.Fatalf("IsWinningHand 返回错误：%v", err)
				}
				if tc.IsWin != nil && isWin != *tc.IsWin {
					t.Fatalf("IsWinningHand = %v，Python 期望 %v", isWin, *tc.IsWin)
				}
			case "wait":
				if tc.Size != TenpaiHandSize {
					t.Fatalf("13 张用例的 size 字段是 %d", tc.Size)
				}
				waits, err := WaitingTiles(hand)
				if err != nil {
					t.Fatalf("WaitingTiles 返回错误：%v", err)
				}
				want := parseFixtureHand(t, tc.Waits, false)
				if !slices.Equal(waits, want) {
					t.Fatalf("WaitingTiles = %v，Python 期望 %v", waits, want)
				}
			default:
				t.Fatalf("未知 check 类型：%q", tc.Check)
			}
		})
	}
}

// TestRandomCases 用 Python 生成的 2000 条随机手牌做逐条对拍。
func TestRandomCases(t *testing.T) {
	fixture := loadFixture(t, "random_cases.json")
	if len(fixture.Cases) != 2000 {
		t.Fatalf("随机用例数是 %d，期望 2000", len(fixture.Cases))
	}

	var wantWin, wantNotWin, wantWait, wantNoWait int
	for _, tc := range fixture.Cases {
		hand := parseFixtureHand(t, tc.Tiles, false)
		switch tc.Check {
		case "win":
			pattern, err := WinningPattern(hand)
			if err != nil {
				t.Fatalf("用例 %s（%s）WinningPattern 返回错误：%v", tc.ID, tc.Kind, err)
			}
			if want := wantPattern(tc); pattern != want {
				t.Fatalf("用例 %s（%s）WinningPattern = %q，Python 期望 %q", tc.ID, tc.Kind, pattern, want)
			}
			isWin, err := IsWinningHand(hand)
			if err != nil {
				t.Fatalf("用例 %s（%s）IsWinningHand 返回错误：%v", tc.ID, tc.Kind, err)
			}
			if tc.IsWin != nil && isWin != *tc.IsWin {
				t.Fatalf("用例 %s（%s）IsWinningHand = %v，Python 期望 %v", tc.ID, tc.Kind, isWin, *tc.IsWin)
			}
			if isWin {
				wantWin++
			} else {
				wantNotWin++
			}
		case "wait":
			if tc.Size != TenpaiHandSize {
				t.Fatalf("用例 %s 的 size 字段是 %d", tc.ID, tc.Size)
			}
			waits, err := WaitingTiles(hand)
			if err != nil {
				t.Fatalf("用例 %s（%s）WaitingTiles 返回错误：%v", tc.ID, tc.Kind, err)
			}
			want := parseFixtureHand(t, tc.Waits, false)
			if !slices.Equal(waits, want) {
				t.Fatalf("用例 %s（%s）WaitingTiles = %v，Python 期望 %v", tc.ID, tc.Kind, waits, want)
			}
			if len(waits) > 0 {
				wantWait++
			} else {
				wantNoWait++
			}
		default:
			t.Fatalf("用例 %s 的 check 类型未知：%q", tc.ID, tc.Check)
		}
	}

	// 覆盖度自检：样本里必须同时有胡 / 不胡、有听 / 无听，否则对拍没有意义。
	if wantWin < 500 || wantNotWin < 500 {
		t.Fatalf("正负样本覆盖不足：胡=%d，不胡=%d", wantWin, wantNotWin)
	}
	if wantWait < 400 || wantNoWait < 100 {
		t.Fatalf("听牌样本覆盖不足：有听=%d，无听=%d", wantWait, wantNoWait)
	}
	if wantWin+wantNotWin+wantWait+wantNoWait != len(fixture.Cases) {
		t.Fatalf("统计数与用例总数不符")
	}
	t.Logf("对拍通过：%d 条随机用例（胡=%d，不胡=%d，有听=%d，无听=%d）",
		len(fixture.Cases), wantWin, wantNotWin, wantWait, wantNoWait)
}

// ---------------------------------------------------------------------------
// 牌定义、解析与牌墙
// ---------------------------------------------------------------------------

func TestTileNamesAndParseTile(t *testing.T) {
	if len(TileNames) != NumTileKinds {
		t.Fatalf("TileNames 长度 = %d，期望 %d", len(TileNames), NumTileKinds)
	}
	seen := make(map[Tile]struct{}, NumTileKinds)
	for index, name := range TileNames {
		tile, err := ParseTile(name)
		if err != nil {
			t.Fatalf("ParseTile(%q) 失败：%v", name, err)
		}
		if tile != Tile(index) {
			t.Fatalf("ParseTile(%q) = %d，期望下标 %d", name, tile, index)
		}
		if tile.String() != name {
			t.Fatalf("Tile(%d).String() = %q，期望 %q", tile, tile.String(), name)
		}
		seen[tile] = struct{}{}
	}
	if len(seen) != NumTileKinds {
		t.Fatalf("牌种去重后只有 %d 种，期望 %d", len(seen), NumTileKinds)
	}

	for _, bad := range []string{"0m", "10m", "1M", "X", "", "E "} {
		if _, err := ParseTile(bad); err == nil {
			t.Fatalf("ParseTile(%q) 应当返回错误", bad)
		} else if !errors.Is(err, ErrInvalidHand) {
			t.Fatalf("ParseTile(%q) 的错误应匹配 ErrInvalidHand，实际 %v", bad, err)
		}
	}
	if Tile(NumTileKinds).String() != "Tile(34)" {
		t.Fatalf("越界牌的 String() = %q", Tile(NumTileKinds).String())
	}
}

func TestParseHand(t *testing.T) {
	cases := []struct {
		text string
		want []Tile
	}{
		{"", []Tile{}},
		{"1m", []Tile{Tile1m}},
		{"123m 456p EE", []Tile{Tile1m, Tile2m, Tile3m, Tile4p, Tile5p, Tile6p, TileEast, TileEast}},
		{"1112345678999m", []Tile{Tile1m, Tile1m, Tile1m, Tile2m, Tile3m, Tile4m, Tile5m, Tile6m, Tile7m, Tile8m, Tile9m, Tile9m, Tile9m}},
		{"ESWN", []Tile{TileEast, TileSouth, TileWest, TileNorth}},
	}
	for _, tc := range cases {
		got, err := ParseHand(tc.text)
		if err != nil {
			t.Fatalf("ParseHand(%q) 失败：%v", tc.text, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("ParseHand(%q) = %v，期望 %v", tc.text, got, tc.want)
		}
	}
	for _, bad := range []string{"1m 0m", "XYZ"} {
		if _, err := ParseHand(bad); err == nil {
			t.Fatalf("ParseHand(%q) 应当返回错误", bad)
		}
	}
}

func TestBuildDeck(t *testing.T) {
	deck := BuildDeck()
	if len(deck) != DeckSize {
		t.Fatalf("牌墙长度 = %d，期望 %d", len(deck), DeckSize)
	}
	counts := make(map[Tile]int, NumTileKinds)
	for _, tile := range deck {
		counts[tile]++
	}
	if len(counts) != NumTileKinds {
		t.Fatalf("牌种数 = %d，期望 %d", len(counts), NumTileKinds)
	}
	for tile := Tile(0); tile < NumTileKinds; tile++ {
		if counts[tile] != TilesPerKind {
			t.Fatalf("牌 %q 有 %d 张，期望 %d 张", tile, counts[tile], TilesPerKind)
		}
	}
	// 顺序按 TileNames 连续排列。
	for index, tile := range deck {
		if want := Tile(index / TilesPerKind); tile != want {
			t.Fatalf("deck[%d] = %q，期望 %q", index, tile, want)
		}
	}
	deck2 := BuildDeck()
	if &deck[0] == &deck2[0] {
		t.Fatal("BuildDeck 应当每次返回新切片")
	}
}

func TestShuffle(t *testing.T) {
	original := BuildDeck()
	before := slices.Clone(original)

	first := Shuffle(original, 42)
	second := Shuffle(original, 42)
	if !slices.Equal(first, second) {
		t.Fatal("相同 seed 的洗牌结果必须一致")
	}
	if slices.Equal(first, before) {
		t.Fatal("洗牌后不应与原牌墙完全相同（seed=42）")
	}
	if !slices.Equal(original, before) {
		t.Fatal("Shuffle 不得修改入参")
	}

	// 不同 seed 应当得到不同排列（概率上几乎必然）。
	if slices.Equal(first, Shuffle(original, 43)) {
		t.Fatal("不同 seed 的洗牌结果不应完全相同")
	}

	// 洗牌只换顺序，不增删牌：排序后必须与原牌墙一致。
	sortedFirst := slices.Clone(first)
	slices.Sort(sortedFirst)
	if !slices.Equal(sortedFirst, before) {
		t.Fatal("洗牌后牌的多重集合必须与原牌墙一致")
	}
}

func TestDrawTile(t *testing.T) {
	wall := []Tile{Tile1m, Tile2m, Tile3m}
	tile, rest, ok := DrawTile(wall)
	if !ok {
		t.Fatal("非空牌墙应当摸到牌")
	}
	if tile != Tile3m {
		t.Fatalf("摸到的牌 = %q，期望最后一张 3m", tile)
	}
	if len(rest) != 2 || !slices.Equal(rest, []Tile{Tile1m, Tile2m}) {
		t.Fatalf("剩余牌墙 = %v", rest)
	}
	if len(wall) != 3 {
		t.Fatal("DrawTile 不得修改入参牌墙")
	}
	if _, rest, ok := DrawTile(nil); ok || len(rest) != 0 {
		t.Fatal("空牌墙应当返回 ok=false")
	}
}

func TestDealHands(t *testing.T) {
	wall := Shuffle(BuildDeck(), 7)
	before := slices.Clone(wall)
	hands, rest, err := DealHands(wall, nil)
	if err != nil {
		t.Fatalf("发牌失败：%v", err)
	}
	if !slices.Equal(wall, before) {
		t.Fatal("DealHands 不得修改入参牌墙")
	}
	if len(rest) != DeckSize-(TenpaiHandSize*len(DefaultSeats)+1) {
		t.Fatalf("剩余牌墙 = %d 张，期望 83 张", len(rest))
	}
	total := 0
	for index, seat := range DefaultSeats {
		hand, ok := hands[seat]
		if !ok {
			t.Fatalf("缺少座位 %q 的手牌", seat)
		}
		wantSize := TenpaiHandSize
		if index == 0 {
			wantSize = WinHandSize
		}
		if len(hand) != wantSize {
			t.Fatalf("座位 %q 手牌 %d 张，期望 %d 张", seat, len(hand), wantSize)
		}
		if !slices.IsSorted(hand) {
			t.Fatalf("座位 %q 的手牌未排序：%v", seat, hand)
		}
		total += len(hand)
	}
	if total != 53 {
		t.Fatalf("四家手牌共 %d 张，期望 53 张", total)
	}

	// 自定义两家：庄家 14 张、另一家 13 张，剩 109 张。
	twoHands, rest2, err := DealHands(wall, []string{"east", "west"})
	if err != nil {
		t.Fatalf("两家发牌失败：%v", err)
	}
	if len(twoHands) != 2 || len(twoHands["east"]) != 14 || len(twoHands["west"]) != 13 {
		t.Fatalf("两家手牌张数不对：%v", twoHands)
	}
	if len(rest2) != DeckSize-27 {
		t.Fatalf("两家发牌后剩余 %d 张，期望 109 张", len(rest2))
	}

	// 牌墙不足：4 家需要 53 张。
	if _, _, err := DealHands(BuildDeck()[:52], nil); !errors.Is(err, ErrNotEnoughTiles) {
		t.Fatalf("牌墙不足应返回 ErrNotEnoughTiles，实际 %v", err)
	}
	if _, _, err := DealHands(BuildDeck(), []string{}); !errors.Is(err, ErrMahjong) {
		t.Fatalf("空座位列表应返回 ErrMahjong，实际 %v", err)
	}
	if _, _, err := DealHands(BuildDeck(), []string{"east", "east"}); !errors.Is(err, ErrMahjong) {
		t.Fatalf("重复座位应返回 ErrMahjong，实际 %v", err)
	}
}

func TestSortHandAndCountTiles(t *testing.T) {
	hand, err := ParseHand("9s 1m E 1m 5p")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	original := slices.Clone(hand)
	sorted, err := SortHand(hand)
	if err != nil {
		t.Fatalf("排序失败：%v", err)
	}
	want := []Tile{Tile1m, Tile1m, Tile5p, Tile9s, TileEast}
	if !slices.Equal(sorted, want) {
		t.Fatalf("SortHand = %v，期望 %v", sorted, want)
	}
	if !slices.Equal(hand, original) {
		t.Fatal("SortHand 不得修改入参")
	}

	counts, err := CountTiles(hand)
	if err != nil {
		t.Fatalf("CountTiles 失败：%v", err)
	}
	if counts[Tile1m] != 2 || counts[Tile5p] != 1 || counts[TileEast] != 1 || counts[Tile9s] != 1 {
		t.Fatalf("计数结果不对：%v", counts)
	}
	if counts[Tile2m] != 0 {
		t.Fatalf("未出现的牌计数应为 0，实际 %d", counts[Tile2m])
	}
	sum := 0
	for _, count := range counts {
		sum += int(count)
	}
	if sum != len(hand) {
		t.Fatalf("计数总和 %d 与手牌张数 %d 不一致", sum, len(hand))
	}

	// 非法手牌应当被排序 / 计数接口拒绝。
	bad := []Tile{Tile1m, Tile1m, Tile1m, Tile1m, Tile1m}
	if _, err := SortHand(bad); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("SortHand 对 5 张 1m 应返回 ErrInvalidHand，实际 %v", err)
	}
	if _, err := CountTiles(bad); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("CountTiles 对 5 张 1m 应返回 ErrInvalidHand，实际 %v", err)
	}
}

func TestValidateHand(t *testing.T) {
	hand, err := ParseHand("123m 456p 789s E S W N")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(hand) != TenpaiHandSize {
		t.Fatalf("测试手牌应为 13 张，实际 %d 张", len(hand))
	}
	if err := ValidateHand(hand); err != nil {
		t.Fatalf("合法手牌不应报错：%v", err)
	}
	if err := ValidateHand(hand, WinHandSize); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("13 张按 14 张校验应报错，实际 %v", err)
	}
	if err := ValidateHand(hand, 13, 14); err != nil {
		t.Fatalf("多候选张数时应通过：%v", err)
	}
	if err := ValidateHand([]Tile{Tile1m, Tile(NumTileKinds)}); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("未知牌应报错，实际 %v", err)
	}
	if err := ValidateHand([]Tile{Tile1m, Tile1m, Tile1m, Tile1m, Tile1m}); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("同种 5 张应报错，实际 %v", err)
	}
	if !strings.Contains(ValidateHand(hand, WinHandSize).Error(), "14") {
		t.Fatal("张数错误信息里应包含期望张数 14")
	}
}

// ---------------------------------------------------------------------------
// 规则边界：非法输入与契约里点名的关键语义
// ---------------------------------------------------------------------------

func TestWinningPatternInvalidInput(t *testing.T) {
	base, err := ParseHand("123456789m 123p 99s")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	for _, size := range []int{0, 1, 13, 15} {
		var hand []Tile
		if size <= len(base) {
			hand = slices.Clone(base[:size])
		} else {
			hand = append(slices.Clone(base), Tile1m)
		}
		if _, err := WinningPattern(hand); !errors.Is(err, ErrInvalidHand) {
			t.Fatalf("%d 张手牌应报 ErrInvalidHand，实际 %v", size, err)
		}
		if _, err := IsWinningHand(hand); !errors.Is(err, ErrInvalidHand) {
			t.Fatalf("%d 张手牌 IsWinningHand 应报 ErrInvalidHand，实际 %v", size, err)
		}
	}

	unknown := append(slices.Clone(base), Tile(NumTileKinds))
	if _, err := WinningPattern(unknown); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("未知牌应报 ErrInvalidHand，实际 %v", err)
	}
	over := []Tile{
		Tile1m, Tile1m, Tile1m, Tile1m, Tile1m, // 5 张 1m，超过每种 4 张上限
		Tile2m, Tile3m, Tile4m, Tile5m, Tile6m, Tile7m, Tile8m, Tile9m, Tile1p,
	}
	if _, err := WinningPattern(over); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("同种超 4 张应报 ErrInvalidHand，实际 %v", err)
	}
}

func TestWaitingTilesInvalidInput(t *testing.T) {
	base, err := ParseHand("123m 456m 789m 11p 45s")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	for _, size := range []int{0, 1, 12, 14} {
		var hand []Tile
		if size <= len(base) {
			hand = slices.Clone(base[:size])
		} else {
			hand = append(slices.Clone(base), Tile1m)
		}
		if _, err := WaitingTiles(hand); !errors.Is(err, ErrInvalidHand) {
			t.Fatalf("%d 张手牌应报 ErrInvalidHand，实际 %v", size, err)
		}
	}
	if _, err := WaitingTiles(append(slices.Clone(base), Tile(NumTileKinds))); !errors.Is(err, ErrInvalidHand) {
		t.Fatalf("未知牌应报 ErrInvalidHand，实际 %v", err)
	}

	// 纯函数：不得修改入参。
	original := slices.Clone(base)
	if _, err := WaitingTiles(base); err != nil {
		t.Fatalf("WaitingTiles 失败：%v", err)
	}
	if !slices.Equal(base, original) {
		t.Fatal("WaitingTiles 不得修改入参")
	}
}

// TestWaitingTilesNineGates 覆盖契约点名的「九面听」压力用例。
func TestWaitingTilesNineGates(t *testing.T) {
	hand, err := ParseHand("1112345678999m")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	waits, err := WaitingTiles(hand)
	if err != nil {
		t.Fatalf("WaitingTiles 失败：%v", err)
	}
	want := make([]Tile, 0, 9)
	for tile := Tile1m; tile <= Tile9m; tile++ {
		want = append(want, tile)
	}
	if !slices.Equal(waits, want) {
		t.Fatalf("九莲宝灯听牌 = %v，期望 1m~9m", waits)
	}
}

// TestWaitingTilesSkipsFourCopies 覆盖「手牌已有 4 张的牌种不能被推荐为听牌」。
func TestWaitingTilesSkipsFourCopies(t *testing.T) {
	hand, err := ParseHand("123m 456m 789m 9999s")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	waits, err := WaitingTiles(hand)
	if err != nil {
		t.Fatalf("WaitingTiles 失败：%v", err)
	}
	if len(waits) != 0 {
		t.Fatalf("手牌已有 4 张 9s 时不应有听牌，实际 %v", waits)
	}
}

// TestSevenPairsPriority 覆盖「七对与标准型同时成立时返回 standard」的契约选择。
func TestSevenPairsPriority(t *testing.T) {
	hand, err := ParseHand("11223344556677m")
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	pattern, err := WinningPattern(hand)
	if err != nil {
		t.Fatalf("WinningPattern 失败：%v", err)
	}
	if pattern != PatternStandard {
		t.Fatalf("WinningPattern = %q，期望 standard（标准型优先）", pattern)
	}
}
