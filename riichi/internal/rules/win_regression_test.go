package rules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// v1Case 对应 riichi/testdata/v1_cases.json 的单条用例。
//
// 字段说明（转换规则见 fixture 的 meta.notes）：
//   - invalid=true：v1 里该输入是非法手牌，新接口表达为 Decompose=false / Shanten=ShantenInvalid；
//   - pattern：v1 对 14 张手牌的牌型期望（standard / seven_pairs / null）；
//   - waits：v1 对 13 张手牌的听牌期望（新契约没有 WaitingTiles，用 Decompose 反推后对比）；
//   - winning：仅 win 用例使用，取 tiles 最后一张，用于确定待ち类型；
//   - "?"：v1 用 0m 表示未知牌，日麻里 0m 是赤 5m，故转换成 Kind 越界的哨兵。
type v1Case struct {
	ID      string   `json:"id"`
	Check   string   `json:"check"`
	Hand    string   `json:"hand"`
	Tiles   []string `json:"tiles"`
	Size    int      `json:"size"`
	Pattern *string  `json:"pattern"`
	IsWin   *bool    `json:"is_win"`
	Waits   []string `json:"waits"`
	Invalid bool     `json:"invalid"`
	Winning string   `json:"winning"`
}

// v1Fixture 是 v1 回归 fixture 的顶层结构。
type v1Fixture struct {
	Meta struct {
		Total        int `json:"total"`
		WinCases     int `json:"win_cases"`
		WaitCases    int `json:"wait_cases"`
		InvalidCases int `json:"invalid_cases"`
	} `json:"meta"`
	Cases []v1Case `json:"cases"`
}

// loadV1Fixture 读取 riichi/testdata/v1_cases.json。
func loadV1Fixture(t *testing.T) v1Fixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "v1_cases.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 v1 fixture 失败（%s）：%v", path, err)
	}
	var fixture v1Fixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("解析 v1 fixture 失败（%s）：%v", path, err)
	}
	return fixture
}

// TestV1FixtureIntegrity 校验 fixture 结构完整：条数、类型分布、ID 唯一。
func TestV1FixtureIntegrity(t *testing.T) {
	fixture := loadV1Fixture(t)
	if len(fixture.Cases) != fixture.Meta.Total || fixture.Meta.Total != 39 {
		t.Fatalf("用例数 = %d（meta.total=%d），期望 39", len(fixture.Cases), fixture.Meta.Total)
	}

	winCount, waitCount, invalidCount := 0, 0, 0
	seen := make(map[string]bool, len(fixture.Cases))
	for _, tc := range fixture.Cases {
		if seen[tc.ID] {
			t.Fatalf("用例 ID 重复：%q", tc.ID)
		}
		seen[tc.ID] = true
		switch tc.Check {
		case "win":
			winCount++
		case "wait":
			waitCount++
		default:
			t.Fatalf("用例 %q 的 check 未知：%q", tc.ID, tc.Check)
		}
		if tc.Invalid {
			invalidCount++
		}
		if tc.Size != len(tc.Tiles) {
			t.Fatalf("用例 %q 的 size=%d 与 tiles 数 %d 不一致", tc.ID, tc.Size, len(tc.Tiles))
		}
	}
	if winCount != fixture.Meta.WinCases || winCount != 23 {
		t.Fatalf("win 用例数 = %d，期望 23", winCount)
	}
	if waitCount != fixture.Meta.WaitCases || waitCount != 16 {
		t.Fatalf("wait 用例数 = %d，期望 16", waitCount)
	}
	if invalidCount != fixture.Meta.InvalidCases || invalidCount != 11 {
		t.Fatalf("invalid 用例数 = %d，期望 11", invalidCount)
	}
}

// TestV1FixtureWin 逐条对拍 v1 的判胡结果。
func TestV1FixtureWin(t *testing.T) {
	fixture := loadV1Fixture(t)
	for _, tc := range fixture.Cases {
		if tc.Check != "win" {
			continue
		}
		t.Run(tc.ID, func(t *testing.T) {
			hand := parseTileNames(t, tc.Tiles)
			winning := tileFromName(t, tc.Winning)

			if tc.Invalid {
				if shape, ok := DecomposeWithOptions(hand, nil, winning, CompatOptions()); ok {
					t.Fatalf("非法手牌不该判和，实际拆解出 %+v", shape)
				}
				if IsWinningWithOptions(hand, nil, winning, CompatOptions()) {
					t.Fatal("IsWinning 对非法手牌应返回 false")
				}
				// 张数 13 的用例在 Shanten 看来是合法听牌形，其余非法输入必须被 Shanten 拒绝。
				if tc.Size != tenpaiHandSize {
					if got := ShantenWithOptions(hand, 0, CompatOptions()); got != ShantenInvalid {
						t.Fatalf("Shanten = %d，期望 ShantenInvalid(%d)", got, ShantenInvalid)
					}
				}
				return
			}

			// v1 的 not_win 用例：14 张合法手牌但不和牌。
			if tc.Pattern == nil {
				if tc.IsWin != nil && *tc.IsWin {
					t.Fatal("fixture 的 pattern=null 与 is_win=true 矛盾")
				}
				if shape, ok := DecomposeWithOptions(hand, nil, winning, CompatOptions()); ok {
					t.Fatalf("v1 期望不和牌，实际拆解出 %+v", shape)
				}
				if IsWinningWithOptions(hand, nil, winning, CompatOptions()) {
					t.Fatal("IsWinning 应为 false")
				}
				// 14 张里拿掉任意一张后，13 张手牌的向听数必须是合法值（0..6）。
				for remove := range hand {
					rest := append(cloneTiles(hand[:remove]), hand[remove+1:]...)
					if got := ShantenWithOptions(rest, 0, CompatOptions()); got < 0 {
						t.Fatalf("拿掉第 %d 张后的向听数 = %d，期望 >= 0", remove, got)
					}
				}
				return
			}

			shape, ok := DecomposeWithOptions(hand, nil, winning, CompatOptions())
			if !ok {
				t.Fatalf("v1 期望判和，实际未判和（hand=%q）", tc.Hand)
			}
			if shape.Pattern != *tc.Pattern {
				t.Fatalf("Pattern = %q，v1 期望 %q", shape.Pattern, *tc.Pattern)
			}
			if tc.IsWin != nil && !*tc.IsWin {
				t.Fatal("fixture 的 is_win 与 pattern 矛盾")
			}
			if !IsWinningWithOptions(hand, nil, winning, CompatOptions()) {
				t.Fatal("IsWinning 与 Decompose 结果不一致")
			}
			verifyWinShape(t, hand, shape, winning)

			// 从和牌里拿掉一张和了牌，剩下的 13 张必须听牌（向听 0）。
			if got := ShantenWithOptions(removeOneKind(t, hand, winning.Kind), 0, CompatOptions()); got != 0 {
				t.Fatalf("拿掉和了牌后的向听 = %d，期望 0", got)
			}

			// 若 v1 的手牌简写能被 riichi 的 ParseHand 解析，顺带验证解析结果一致。
			if tc.Hand != "" {
				if parsed, err := tile.ParseHand(tc.Hand); err == nil && !slices.Equal(parsed, hand) {
					t.Fatalf("ParseHand(%q) = %v，fixture tiles = %v", tc.Hand, parsed, hand)
				}
			}
		})
	}
}

// TestV1FixtureWait 逐条对拍 v1 的听牌结果。
//
// 新契约没有 WaitingTiles：这里对 34 种牌逐一试加一张调用 Decompose，
// 能判和的就是听牌；手牌已有 4 张的牌种跳过（牌山没有第 5 张），与 v1 语义一致。
func TestV1FixtureWait(t *testing.T) {
	fixture := loadV1Fixture(t)
	for _, tc := range fixture.Cases {
		if tc.Check != "wait" {
			continue
		}
		t.Run(tc.ID, func(t *testing.T) {
			hand := parseTileNames(t, tc.Tiles)

			if tc.Invalid {
				if got := ShantenWithOptions(hand, 0, CompatOptions()); got != ShantenInvalid {
					t.Fatalf("非法手牌的 Shanten = %d，期望 ShantenInvalid(%d)", got, ShantenInvalid)
				}
				return
			}

			if len(hand) != tenpaiHandSize {
				t.Fatalf("听牌用例应为 13 张，实际 %d 张", len(hand))
			}

			counts := kindCounts(hand)
			got := make([]tile.Tile, 0, 13)
			for kind := 0; kind < tile.NumKinds; kind++ {
				if counts[kind] >= tile.CopiesPerKind {
					continue
				}
				candidate := append(cloneTiles(hand), tile.Tile{Kind: tile.Kind(kind)})
				if _, ok := DecomposeWithOptions(candidate, nil, tile.Tile{Kind: tile.Kind(kind)}, CompatOptions()); ok {
					got = append(got, tile.Tile{Kind: tile.Kind(kind)})
				}
			}
			want := parseTileNames(t, tc.Waits)
			if !slices.Equal(got, want) {
				t.Fatalf("反推听牌 = %v，v1 期望 %v", got, want)
			}

			shanten := ShantenWithOptions(hand, 0, CompatOptions())
			if len(want) > 0 && shanten != 0 {
				t.Fatalf("有听牌时向听 = %d，期望 0", shanten)
			}
			if len(want) == 0 && shanten < 0 {
				t.Fatalf("无听牌时向听 = %d，不应为负", shanten)
			}
			// 顺带校验手牌简写：能解析则必须与 tiles 一致。
			if tc.Hand != "" {
				if parsed, err := tile.ParseHand(tc.Hand); err == nil && !slices.Equal(parsed, hand) {
					t.Fatalf("ParseHand(%q) = %v，fixture tiles = %v", tc.Hand, parsed, hand)
				}
			}
		})
	}
}
