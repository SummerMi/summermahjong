package game

import (
	"summermahjong/riichi/internal/tile"
)

// MeldKind 是副露种类（契约 §2.3）。
type MeldKind string

const (
	MeldChi           MeldKind = "chi"            // 吃
	MeldPon           MeldKind = "pon"            // 碰
	MeldKanDaiminkan  MeldKind = "kan_daiminkan"  // 大明槓
	MeldKanShouminkan MeldKind = "kan_shouminkan" // 加槓
	MeldKanAnkan      MeldKind = "kan_ankan"      // 暗槓
)

// Meld 是一组已经亮出来的面子。
type Meld struct {
	Kind  MeldKind
	Tiles []tile.Tile
	From  Seat // 被副露的那张牌来自哪家；暗杠为自身
}

// Observation 是发给 Agent 的唯一输入（契约 §2.5）。
//
// 安全约束：这里不得出现其他三家的手牌、牌山顺序、裏宝牌指示牌。
// 由 Observe 统一裁剪，Agent 拿不到就无从作弊。
type Observation struct {
	Seat           Seat
	Hand           []tile.Tile
	Drawn          *tile.Tile
	Melds          [4][]Meld
	Discards       [4][]tile.Tile
	DoraIndicators []tile.Tile
	Scores         [4]int
	Riichi         [4]bool
	WallRemaining  int
	RoundWind      Wind
	Kyoku          int
	Honba          int
	RiichiSticks   int
	LegalActions   []Action
}

// Observe 构造某个座位视角的观察结果（只含公开信息 + 自己的手牌）。
func (s *State) Observe(seat Seat) Observation {
	obs := Observation{
		Seat:           seat,
		Hand:           cloneTiles(s.Hands[seat]),
		DoraIndicators: s.Wall.DoraIndicators(),
		Scores:         s.Scores,
		Riichi:         s.Riichi,
		WallRemaining:  s.Wall.Remaining(),
		RoundWind:      s.RoundWind,
		Kyoku:          s.Kyoku,
		Honba:          s.Honba,
		RiichiSticks:   s.RiichiStix,
	}
	if s.Drawn != nil {
		drawn := *s.Drawn
		obs.Drawn = &drawn
	}
	for i := 0; i < 4; i++ {
		obs.Melds[i] = cloneMelds(s.Melds[i])
		obs.Discards[i] = cloneTiles(s.Discards[i])
	}
	// 只有轮到自己时才给合法动作；响应窗口在 M2 扩展。
	if s.Turn == seat {
		obs.LegalActions = s.Legal()
	}
	return obs
}

// cloneTiles 复制牌切片，避免 Agent 通过别名改到引擎状态。
func cloneTiles(src []tile.Tile) []tile.Tile {
	if src == nil {
		return nil
	}
	out := make([]tile.Tile, len(src))
	copy(out, src)
	return out
}

// cloneMelds 复制副露切片（含内部 Tiles）。
func cloneMelds(src []Meld) []Meld {
	if src == nil {
		return nil
	}
	out := make([]Meld, len(src))
	for i, m := range src {
		out[i] = Meld{Kind: m.Kind, From: m.From, Tiles: cloneTiles(m.Tiles)}
	}
	return out
}
