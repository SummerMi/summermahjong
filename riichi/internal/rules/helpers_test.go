package rules

import (
	"math/rand"
	"slices"
	"testing"

	"summermahjong/riichi/internal/tile"
)

// mustParseHand 解析牌面简写（"123m 456p 789s 11z"），失败直接终止测试。
func mustParseHand(t *testing.T, text string) []tile.Tile {
	t.Helper()
	hand, err := tile.ParseHand(text)
	if err != nil {
		t.Fatalf("解析手牌 %q 失败：%v", text, err)
	}
	return hand
}

// tileFromName 解析单张牌面；"?" 是非法牌哨兵（Kind 越界），用于构造非法输入用例。
func tileFromName(t *testing.T, name string) tile.Tile {
	t.Helper()
	if name == "?" {
		return tile.Tile{Kind: tile.NumKinds}
	}
	one, err := tile.ParseTile(name)
	if err != nil {
		t.Fatalf("解析牌 %q 失败：%v", name, err)
	}
	return one
}

// parseTileNames 批量解析牌面文本。
func parseTileNames(t *testing.T, names []string) []tile.Tile {
	t.Helper()
	tiles := make([]tile.Tile, 0, len(names))
	for _, name := range names {
		tiles = append(tiles, tileFromName(t, name))
	}
	return tiles
}

// kindCounts 统计手牌中每种牌的张数（忽略赤宝牌标记，赤 5 与普通 5 同 Kind）。
func kindCounts(hand []tile.Tile) [tile.NumKinds]int {
	var c [tile.NumKinds]int
	for _, t := range hand {
		if t.Kind < tile.NumKinds {
			c[t.Kind]++
		}
	}
	return c
}

// akaCounts 统计手牌中每种牌的赤宝牌张数。
func akaCounts(hand []tile.Tile) [tile.NumKinds]int {
	var c [tile.NumKinds]int
	for _, t := range hand {
		if t.Kind < tile.NumKinds && t.Aka {
			c[t.Kind]++
		}
	}
	return c
}

// validMeldShape 报告一组拆解出的面子是否合法：
// 刻子三张相同、顺子同花色连续三张、杠四张相同（契约 v1.2 起 Tiles 变长）。
func validMeldShape(m MeldShape) bool {
	switch m.Kind {
	case MeldPon:
		return len(m.Tiles) == 3 && isPonTiles(m.Tiles)
	case MeldChi:
		return len(m.Tiles) == 3 && isChiTiles(m.Tiles)
	case MeldKanDaiminkan, MeldKanShouminkan, MeldKanAnkan:
		return len(m.Tiles) == 4 && isPonTiles(m.Tiles)
	default:
		return false
	}
}

// verifyWinShape 校验拆解结果的结构自洽性：
//   - 待ち类型非空；
//   - 标准型恰好 4 组面子 + 将牌 2 张，且用到的牌与入参手牌完全一致（含赤宝牌标记）；
//   - 七对 / 国士没有面子，且手牌确实满足对应牌型。
func verifyWinShape(t *testing.T, hand []tile.Tile, shape WinShape, winning tile.Tile) {
	t.Helper()
	if shape.Wait == "" {
		t.Fatal("待ち类型为空")
	}
	if kindCounts(flattenShape(shape))[winning.Kind] == 0 {
		t.Fatalf("拆解结果里找不到和了牌 %v", winning)
	}

	switch shape.Pattern {
	case PatternStandard:
		if len(shape.Melds) != 4 {
			t.Fatalf("标准型应有 4 组面子，实际 %d 组", len(shape.Melds))
		}
		var got [tile.NumKinds]int
		got[shape.Jantou.Kind] += 2
		prev := -1
		for _, meld := range shape.Melds {
			if !validMeldShape(meld) {
				t.Fatalf("拆解出的面子非法：%+v", meld)
			}
			first := int(meld.Tiles[0].Kind)
			if first < prev {
				t.Fatalf("面子应按首张牌升序排列，实际 %v", shape.Melds)
			}
			prev = first
			for _, mt := range meld.Tiles {
				got[mt.Kind]++
			}
		}
		if want := kindCounts(hand); got != want {
			t.Fatalf("拆解用牌与手牌不一致：拆解 %v，手牌 %v", got, want)
		}
		if gotAka, wantAka := akaCounts(flattenShape(shape)), akaCounts(hand); gotAka != wantAka {
			t.Fatalf("赤宝牌标记没有正确保留：拆解 %v，手牌 %v", gotAka, wantAka)
		}
	case PatternSevenPairs:
		if len(shape.Melds) != 0 {
			t.Fatalf("七对应没有面子，实际 %d 组", len(shape.Melds))
		}
		counts := kindCounts(hand)
		pairs := 0
		for _, n := range counts {
			if n%2 != 0 {
				t.Fatalf("七对手牌里有奇数张的牌种：%v", counts)
			}
			pairs += n / 2
		}
		if pairs != sevenPairsCount {
			t.Fatalf("七对对数 = %d，期望 %d", pairs, sevenPairsCount)
		}
	case PatternKokushi:
		if len(shape.Melds) != 0 {
			t.Fatalf("国士无双没有面子，实际 %d 组", len(shape.Melds))
		}
		counts := kindCounts(hand)
		distinct := 0
		for kind := 0; kind < tile.NumKinds; kind++ {
			if !tile.Kind(kind).IsTerminalOrHonor() && counts[kind] != 0 {
				t.Fatalf("国士无双不应含中张，实际包含 %v", tile.Kind(kind))
			}
			if counts[kind] > 0 {
				distinct++
			}
		}
		if distinct != kokushiKinds {
			t.Fatalf("国士无双应有 %d 种幺九字牌，实际 %d 种", kokushiKinds, distinct)
		}
	default:
		t.Fatalf("未知牌型：%q", shape.Pattern)
	}
}

// flattenShape 把拆解结果展开成牌列表（将牌 2 张 + 所有面子），便于比较赤宝牌标记。
func flattenShape(shape WinShape) []tile.Tile {
	tiles := []tile.Tile{shape.Jantou, shape.Jantou}
	for _, meld := range shape.Melds {
		tiles = append(tiles, meld.Tiles[:]...)
	}
	return tiles
}

// removeOneKind 从手牌里拿掉一张指定牌种，返回新切片（不修改入参）。
func removeOneKind(t *testing.T, hand []tile.Tile, kind tile.Kind) []tile.Tile {
	t.Helper()
	out := make([]tile.Tile, 0, len(hand)-1)
	removed := false
	for _, tl := range hand {
		if !removed && tl.Kind == kind {
			removed = true
			continue
		}
		out = append(out, tl)
	}
	if !removed {
		t.Fatalf("手牌里没有 %v，无法拿掉一张", kind)
	}
	return out
}

// randomHand 用固定随机源生成一手牌面合法的随机手牌（每种不超过 4 张）。
func randomHand(rng *rand.Rand, size int) []tile.Tile {
	var c [tile.NumKinds]int
	hand := make([]tile.Tile, 0, size)
	for len(hand) < size {
		kind := rng.Intn(tile.NumKinds)
		if c[kind] >= tile.CopiesPerKind {
			continue
		}
		c[kind]++
		hand = append(hand, tile.Tile{Kind: tile.Kind(kind)})
	}
	return hand
}

// randomWinHand 构造一手随机的和牌（4 面子 + 1 将），保证每种牌不超过 4 张。
func randomWinHand(t *testing.T, rng *rand.Rand) []tile.Tile {
	t.Helper()
	for attempt := 0; attempt < 1000; attempt++ {
		var c [tile.NumKinds]int
		ok := true
		add := func(kind int, n int) {
			if c[kind]+n > tile.CopiesPerKind {
				ok = false
				return
			}
			c[kind] += n
		}

		pair := rng.Intn(tile.NumKinds)
		add(pair, 2)
		for meld := 0; meld < meldsRequired; meld++ {
			if rng.Intn(2) == 0 {
				add(rng.Intn(tile.NumKinds), 3)
				continue
			}
			base := rng.Intn(3)*9 + rng.Intn(7)
			add(base, 1)
			add(base+1, 1)
			add(base+2, 1)
		}
		if !ok {
			continue
		}
		hand := make([]tile.Tile, 0, winHandSize)
		for kind := 0; kind < tile.NumKinds; kind++ {
			for i := 0; i < c[kind]; i++ {
				hand = append(hand, tile.Tile{Kind: tile.Kind(kind)})
			}
		}
		return hand
	}
	t.Fatal("无法构造随机的和牌手牌")
	return nil
}

// cloneTiles 复制一份牌切片，用于断言函数不修改入参。
func cloneTiles(tiles []tile.Tile) []tile.Tile {
	return slices.Clone(tiles)
}
