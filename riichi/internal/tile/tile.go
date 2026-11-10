// Package tile 定义日麻（立直麻将）的牌与牌山。
//
// 编码约定与 v1（Python/Go 简化引擎）保持一致，便于迁移与对拍：
//   - 数牌：万 1m-9m、筒 1p-9p、索 1s-9s
//   - 字牌：东 E、南 S、西 W、北 N、白 P、发 F、中 C
//   - 每种 4 张，共 34 种 × 4 = 136 张（不含花牌）
//
// 日麻特有：赤宝牌（红 5），用 Tile.Aka 标记；文本用 "r5m" 表示，接受 "0m" 作为同义写法（天凤习惯）。
//
// 命名遵循 docs/riichi-接口契约-20261110.md §9：白 Haku、发 Hatsu、中 Chun、赤宝牌 Aka，
// 避免「红中」与「赤宝牌」都用 Red 造成的歧义。
package tile

import (
	"errors"
	"fmt"
)

// Suit 是牌的花色。
type Suit uint8

const (
	Man   Suit = iota // 万子
	Pin               // 筒子
	Sou               // 索子
	Honor             // 字牌
)

// String 返回花色的单字母表示。
func (s Suit) String() string {
	switch s {
	case Man:
		return "m"
	case Pin:
		return "p"
	case Sou:
		return "s"
	case Honor:
		return "z"
	default:
		return "?"
	}
}

// NumKinds 是牌的种类数（34）。
const NumKinds = 34

// CopiesPerKind 是每种牌的张数（4）。
const CopiesPerKind = 4

// DeckSize 是一副牌的张数（136）。
const DeckSize = NumKinds * CopiesPerKind

// Kind 是牌的种类，取值 0..33。
//
// 0-8 万 1-9，9-17 筒 1-9，18-26 索 1-9，27-33 字牌 东南西北白发中。
type Kind uint8

// 34 种牌。字牌顺序固定为 东南西北白发中（与天凤一致）。
const (
	Man1 Kind = iota
	Man2
	Man3
	Man4
	Man5
	Man6
	Man7
	Man8
	Man9

	Pin1
	Pin2
	Pin3
	Pin4
	Pin5
	Pin6
	Pin7
	Pin8
	Pin9

	Sou1
	Sou2
	Sou3
	Sou4
	Sou5
	Sou6
	Sou7
	Sou8
	Sou9

	East  // 东
	South // 南
	West  // 西
	North // 北
	Haku  // 白（契约 §9 命名）
	Hatsu // 发（契约 §9 命名）
	Chun  // 中（契约 §9 命名）
)

// Suit 返回牌的花色。
func (k Kind) Suit() Suit {
	switch {
	case k <= Man9:
		return Man
	case k <= Pin9:
		return Pin
	case k <= Sou9:
		return Sou
	default:
		return Honor
	}
}

// Number 返回数牌的点数 1-9；字牌返回 0。
func (k Kind) Number() int {
	if k.Suit() == Honor {
		return 0
	}
	return int(k%9) + 1
}

// IsHonor 报告是否字牌。
func (k Kind) IsHonor() bool { return k.Suit() == Honor }

// IsTerminal 报告是否幺九牌（数牌 1/9）。
func (k Kind) IsTerminal() bool {
	n := k.Number()
	return n == 1 || n == 9
}

// IsTerminalOrHonor 报告是否幺九牌或字牌（老头牌）。
func (k Kind) IsTerminalOrHonor() bool { return k.IsHonor() || k.IsTerminal() }

// IsSimple 报告是否中张（2-8 的数牌）。
func (k Kind) IsSimple() bool { return !k.IsTerminalOrHonor() }

// IsWind 报告是否风牌（东南西北）。
func (k Kind) IsWind() bool { return k >= East && k <= North }

// IsDragon 报告是否三元牌（白发中）。
func (k Kind) IsDragon() bool { return k >= Haku && k <= Chun }

// IsFive 报告是否数牌 5（赤宝牌只可能是 5）。
func (k Kind) IsFive() bool { return !k.IsHonor() && k.Number() == 5 }

// String 返回牌种类的文本表示，例如 "5m"、"1p"、"E"。
func (k Kind) String() string {
	if k >= NumKinds {
		return "?"
	}
	if k.Suit() == Honor {
		switch k {
		case East:
			return "E"
		case South:
			return "S"
		case West:
			return "W"
		case North:
			return "N"
		case Haku:
			return "P"
		case Hatsu:
			return "F"
		default: // Chun
			return "C"
		}
	}
	return fmt.Sprintf("%d%s", k.Number(), k.Suit())
}

// Tile 是一张具体的牌：种类 + 是否为赤宝牌。
//
// 赤宝牌只可能是数牌 5（5m/5p/5s），并且每种至多 1 张。
type Tile struct {
	Kind Kind
	Aka  bool // 赤宝牌（红 5）；字段名见契约 §9，避免与「中」混淆
}

// String 返回文本表示；赤宝牌带 "r" 前缀，例如 "r5m"。
func (t Tile) String() string {
	if t.Aka {
		return "r" + t.Kind.String()
	}
	return t.Kind.String()
}

// ErrInvalidTile 表示无法解析的牌面文本。
var ErrInvalidTile = errors.New("tile: invalid tile text")

// ParseKind 解析牌种类文本，例如 "5m"、"7z"、"E"（字牌单字母）。
func ParseKind(text string) (Kind, error) {
	// 字牌单字母写法（与 Tile.String 的输出一致，保证往返可解析）
	if len(text) == 1 {
		switch text[0] {
		case 'E', 'e':
			return East, nil
		case 'S', 's':
			return South, nil
		case 'W', 'w':
			return West, nil
		case 'N', 'n':
			return North, nil
		case 'P', 'p':
			return Haku, nil
		case 'F', 'f':
			return Hatsu, nil
		case 'C', 'c':
			return Chun, nil
		}
		return 0, fmt.Errorf("%w: %q", ErrInvalidTile, text)
	}
	if len(text) != 2 {
		return 0, fmt.Errorf("%w: %q", ErrInvalidTile, text)
	}
	num := text[0]
	suit := text[1]
	if num < '1' || num > '9' {
		return 0, fmt.Errorf("%w: %q", ErrInvalidTile, text)
	}
	n := int(num - '0')
	switch suit {
	case 'm', 'M':
		// num 已限制在 '1'..'9'，n-1 必落在 0..8，不需要额外的上界判断。
		return Man1 + Kind(n-1), nil
	case 'p', 'P':
		return Pin1 + Kind(n-1), nil
	case 's', 'S':
		return Sou1 + Kind(n-1), nil
	case 'z', 'Z', 'h', 'H':
		switch n {
		case 1:
			return East, nil
		case 2:
			return South, nil
		case 3:
			return West, nil
		case 4:
			return North, nil
		case 5:
			return Haku, nil
		case 6:
			return Hatsu, nil
		case 7:
			return Chun, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrInvalidTile, text)
}

// ParseTile 解析一张牌，支持 "5m" 与赤宝牌写法 "r5m"、"0m"。
func ParseTile(text string) (Tile, error) {
	switch {
	case len(text) == 3 && (text[0] == 'r' || text[0] == 'R'):
		kind, err := ParseKind(text[1:])
		if err != nil {
			return Tile{}, err
		}
		if !kind.IsFive() {
			return Tile{}, fmt.Errorf("%w: red tile must be a 5, got %q", ErrInvalidTile, text)
		}
		return Tile{Kind: kind, Aka: true}, nil
	case len(text) == 2 && text[0] == '0':
		kind, err := ParseKind("5" + text[1:])
		if err != nil {
			return Tile{}, err
		}
		if !kind.IsFive() {
			return Tile{}, fmt.Errorf("%w: red tile must be a 5, got %q", ErrInvalidTile, text)
		}
		return Tile{Kind: kind, Aka: true}, nil
	default:
		kind, err := ParseKind(text)
		if err != nil {
			return Tile{}, err
		}
		return Tile{Kind: kind}, nil
	}
}

// ParseHand 解析一串牌，例如 "123m456p789s11z"（同一花色内可连写）。
func ParseHand(text string) ([]Tile, error) {
	out := make([]Tile, 0, len(text)/2)
	var pending []byte
	flush := func(suit byte) error {
		for _, digit := range pending {
			kind, err := ParseKind(string([]byte{digit, suit}))
			if err != nil {
				return err
			}
			out = append(out, Tile{Kind: kind})
		}
		pending = pending[:0]
		return nil
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= '0' && c <= '9':
			pending = append(pending, c)
		case c == 'm' || c == 'p' || c == 's' || c == 'z':
			if len(pending) == 0 {
				return nil, fmt.Errorf("%w: %q", ErrInvalidTile, text)
			}
			if err := flush(c); err != nil {
				return nil, err
			}
		case c == ' ' || c == '\t':
			continue
		default:
			return nil, fmt.Errorf("%w: %q", ErrInvalidTile, text)
		}
	}
	if len(pending) > 0 {
		return nil, fmt.Errorf("%w: trailing digits in %q", ErrInvalidTile, text)
	}
	return out, nil
}
