package tile

import (
	"errors"
	"fmt"
)

// 牌山的结构（日麻，136 张）：
//
//	live 122 张：开局发 4×13 = 52 张，剩余 70 张按摸牌顺序消耗
//	dead  14 张：岭上牌 4（索引 0-3）、宝牌指示牌 5（4-8）、裏宝牌指示牌 5（9-13）
//
// 开局翻 1 张宝牌指示牌；每杠一次，翻开下一张指示牌并从王牌摸一张岭上牌。
const (
	DeadWallSize     = 14
	RinshanCount     = 4
	DoraIndicatorCap = 5
	LiveWallSize     = DeckSize - DeadWallSize // 122
	InitialHandSize  = 13
	MaxDrawsFromLive = LiveWallSize - 4*InitialHandSize // 70
)

// ErrInvalidRedCount 表示赤宝牌数量不合法。
var ErrInvalidRedCount = errors.New("tile: red fives must be between 0 and 3")

// BuildDeck 构造一副完整的 136 张牌，redFives 指定赤宝牌数量（0-3）。
//
// 赤宝牌按 5m、5p、5s 的顺序依次标记，与天凤默认的三枚赤 5 一致。
func BuildDeck(redFives int) ([]Tile, error) {
	if redFives < 0 || redFives > 3 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidRedCount, redFives)
	}
	redKinds := []Kind{Man5, Pin5, Sou5}
	deck := make([]Tile, 0, DeckSize)
	for kind := Kind(0); kind < NumKinds; kind++ {
		redAt := -1
		for i, rk := range redKinds {
			if kind == rk && i < redFives {
				redAt = i
			}
		}
		for copyIndex := 0; copyIndex < CopiesPerKind; copyIndex++ {
			aka := false
			if redAt >= 0 && copyIndex == 0 {
				aka = true
			}
			deck = append(deck, Tile{Kind: kind, Aka: aka})
		}
	}
	return deck, nil
}

// Wall 是一副洗好的牌山，记录可摸的活牌与王牌。
type Wall struct {
	live      []Tile
	dead      []Tile
	liveDrawn int
	rinshan   int // 已摸的岭上牌数，也等于已杠次数（最多 4）
}

// NewWall 用 seed 构造牌山：确定性洗牌后切出 14 张王牌。
func NewWall(seed int64, redFives int) (*Wall, error) {
	deck, err := BuildDeck(redFives)
	if err != nil {
		return nil, err
	}
	Shuffle(deck, seed)
	return &Wall{
		live: deck[:LiveWallSize],
		dead: deck[LiveWallSize:],
	}, nil
}

// Deal 发牌：按座位顺序给四家各 13 张，返回手牌并消耗活牌。
func (w *Wall) Deal() ([4][]Tile, error) {
	need := 4 * InitialHandSize
	if len(w.live)-w.liveDrawn < need {
		return [4][]Tile{}, fmt.Errorf("tile: not enough tiles to deal (remaining %d)", len(w.live)-w.liveDrawn)
	}
	var hands [4][]Tile
	for round := 0; round < InitialHandSize; round++ {
		for seat := 0; seat < 4; seat++ {
			hands[seat] = append(hands[seat], w.live[w.liveDrawn])
			w.liveDrawn++
		}
	}
	return hands, nil
}

// Draw 从活牌中摸一张；牌山摸完返回 ok=false。
func (w *Wall) Draw() (Tile, bool) {
	if w.liveDrawn >= len(w.live) {
		return Tile{}, false
	}
	t := w.live[w.liveDrawn]
	w.liveDrawn++
	return t, true
}

// DrawRinshan 摸一张岭上牌（杠后）；最多 4 张。
func (w *Wall) DrawRinshan() (Tile, bool) {
	if w.rinshan >= RinshanCount {
		return Tile{}, false
	}
	t := w.dead[w.rinshan]
	w.rinshan++
	return t, true
}

// DoraIndicators 返回已翻开的宝牌指示牌（开局 1 张，每杠 +1，最多 5 张）。
func (w *Wall) DoraIndicators() []Tile {
	n := 1 + w.rinshan
	if n > DoraIndicatorCap {
		n = DoraIndicatorCap
	}
	out := make([]Tile, n)
	copy(out, w.dead[RinshanCount:RinshanCount+n])
	return out
}

// UraIndicators 返回裏宝牌指示牌（立直和了时按已翻开的数量取用）。
func (w *Wall) UraIndicators() []Tile {
	out := make([]Tile, DoraIndicatorCap)
	copy(out, w.dead[RinshanCount+DoraIndicatorCap:])
	return out
}

// Remaining 返回活牌剩余张数（不含王牌）。
func (w *Wall) Remaining() int { return len(w.live) - w.liveDrawn }

// KanCount 返回已杠次数（已摸岭上牌数）。
func (w *Wall) KanCount() int { return w.rinshan }

// Live 返回活牌副本（测试/回放用）。
func (w *Wall) Live() []Tile {
	out := make([]Tile, len(w.live))
	copy(out, w.live)
	return out
}

// Dead 返回王牌副本（测试/回放用）。
func (w *Wall) Dead() []Tile {
	out := make([]Tile, len(w.dead))
	copy(out, w.dead)
	return out
}
