package game

import "summermahjong/riichi/internal/tile"

// doraKindFromIndicator 返回指示牌对应的宝牌牌种（契约 §2.2 dora_reveal 用）。
//
// 规则：数牌按 1→2→…→9→1 循环；风牌 东→南→西→北→东；三元牌 白→发→中→白。
// 指示牌的赤宝牌标记不影响宝牌推导。
func doraKindFromIndicator(kind tile.Kind) tile.Kind {
	switch {
	case kind.IsHonor():
		switch kind {
		case tile.East:
			return tile.South
		case tile.South:
			return tile.West
		case tile.West:
			return tile.North
		case tile.North:
			return tile.East
		case tile.Haku:
			return tile.Hatsu
		case tile.Hatsu:
			return tile.Chun
		default: // 中
			return tile.Haku
		}
	default:
		suitBase := tile.Kind(int(kind) / 9 * 9)
		next := (int(kind)%9 + 1) % 9
		return tile.Kind(int(suitBase) + next)
	}
}

// doraKinds 把指示牌列表转成宝牌牌种列表。
func doraKinds(indicators []tile.Tile) []tile.Tile {
	out := make([]tile.Tile, 0, len(indicators))
	for _, indicator := range indicators {
		out = append(out, tile.Tile{Kind: doraKindFromIndicator(indicator.Kind)})
	}
	return out
}

// tileStrings 把牌切片转成文本切片（事件 payload 用）。
func tileStrings(tiles []tile.Tile) []string {
	out := make([]string, 0, len(tiles))
	for _, t := range tiles {
		out = append(out, t.String())
	}
	return out
}
