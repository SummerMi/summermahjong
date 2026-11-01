// Package mahjong 实现广东麻将（推倒胡）的纯逻辑引擎。
//
// 本包与 Python 版 app/services/mahjong 语义一致（牌编码见
// docs/engineering-contract.md 第 3 节）：34 种牌、每种 4 张共 136 张、不含花牌；
// 字牌不能组顺子；同种牌超过 4 张视为非法手牌。
//
// 包内不做任何 I/O、不使用全局可变状态，导出函数都可以安全并发调用。
// 出错一律通过 error 返回（Python 版用异常），便于 API 层统一映射成 HTTP 400。
package mahjong

import (
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"strings"
)

// 牌种数量、每种张数与整副牌张数（34 × 4 = 136，不含花牌）。
const (
	// NumTileKinds 是牌种数量：万/筒/条各 9 种 + 字牌 7 种 = 34。
	NumTileKinds = 34
	// TilesPerKind 是每种牌的张数。
	TilesPerKind = 4
	// DeckSize 是整副牌的张数（34 × 4 = 136，不含花牌）。
	DeckSize = NumTileKinds * TilesPerKind
)

// 手牌张数约定：胡牌判定看 14 张，听牌计算看 13 张。
const (
	// WinHandSize 是胡牌判定要求的张数。
	WinHandSize = 14
	// TenpaiHandSize 是听牌计算要求的张数。
	TenpaiHandSize = 13
)

// Tile 是 34 种牌中的一种，取值 0..33，顺序与 TileNames 一致。
//
// 用 uint8 定长下标而不是 Python 的字符串 + dict：牌的比较退化成单字节整数比较，
// 计数可以直接落进 [NumTileKinds]uint8 定长数组，既没有哈希开销也没有装箱。
type Tile uint8

// 34 种牌的固定下标：先万（1m..9m），再筒、条，最后字牌东南西北白发中。
// 顺序即展示 / 排序 / 遍历顺序，rules.go 的「字牌不能组顺子」等判断依赖它。
const (
	Tile1m Tile = iota
	Tile2m
	Tile3m
	Tile4m
	Tile5m
	Tile6m
	Tile7m
	Tile8m
	Tile9m

	Tile1p
	Tile2p
	Tile3p
	Tile4p
	Tile5p
	Tile6p
	Tile7p
	Tile8p
	Tile9p

	Tile1s
	Tile2s
	Tile3s
	Tile4s
	Tile5s
	Tile6s
	Tile7s
	Tile8s
	Tile9s

	TileEast  // E 东风
	TileSouth // S 南风
	TileWest  // W 西风
	TileNorth // N 北风
	TileWhite // P 白板
	TileGreen // F 发财
	TileRed   // C 红中
)

// 编译期断言：TileRed 必须是最后一种牌（下标 33），保证 Tile 取值不越界。
var _ = [1]struct{}{}[int(TileRed)-NumTileKinds+1]

// TileNames 按固定顺序列出 34 种牌的编码：1m..9m、1p..9p、1s..9s、东南西北白发中。
//
// 下标即 Tile 的取值。调用方只应读取这个全局数组，不要修改它。
var TileNames = [NumTileKinds]string{
	"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m",
	"1p", "2p", "3p", "4p", "5p", "6p", "7p", "8p", "9p",
	"1s", "2s", "3s", "4s", "5s", "6s", "7s", "8s", "9s",
	"E", "S", "W", "N", "P", "F", "C",
}

// DefaultSeats 是默认四家座位，顺序固定为东、南、西、北；第 0 个座位是庄家。
// 调用方只应读取这个全局切片，不要修改它。
var DefaultSeats = []string{"east", "south", "west", "north"}

// 领域错误哨兵。调用方用 errors.Is 判断类别，无需依赖具体文案：
//
//	if errors.Is(err, mahjong.ErrInvalidHand) { /* 映射成 HTTP 400 */ }
var (
	// ErrMahjong 是麻将领域错误基类。
	ErrMahjong = errors.New("mahjong: 领域错误")
	// ErrInvalidHand 表示手牌非法：未知牌、张数不对、同种牌超过 4 张。
	ErrInvalidHand = errors.New("mahjong: 手牌非法")
	// ErrNotEnoughTiles 表示牌墙张数不足。
	ErrNotEnoughTiles = errors.New("mahjong: 牌墙张数不足")
)

// InvalidHandError 携带具体原因的手牌非法错误，可用 errors.Is 匹配 ErrInvalidHand。
type InvalidHandError struct {
	msg string
}

// Error 实现 error 接口。
func (e *InvalidHandError) Error() string {
	return "mahjong: 手牌非法：" + e.msg
}

// Is 让 errors.Is(err, ErrInvalidHand) 与 errors.Is(err, ErrMahjong) 同时成立。
func (e *InvalidHandError) Is(target error) bool {
	return target == ErrInvalidHand || target == ErrMahjong
}

// NotEnoughTilesError 携带具体原因的牌墙不足错误。
type NotEnoughTilesError struct {
	msg string
}

// Error 实现 error 接口。
func (e *NotEnoughTilesError) Error() string {
	return "mahjong: 牌墙张数不足：" + e.msg
}

// Is 让 errors.Is(err, ErrNotEnoughTiles) 与 errors.Is(err, ErrMahjong) 同时成立。
func (e *NotEnoughTilesError) Is(target error) bool {
	return target == ErrNotEnoughTiles || target == ErrMahjong
}

// newInvalidHandError 构造带格式化原因的手牌非法错误。
func newInvalidHandError(format string, args ...any) error {
	return &InvalidHandError{msg: fmt.Sprintf(format, args...)}
}

// newNotEnoughTilesError 构造带格式化原因的牌墙不足错误。
func newNotEnoughTilesError(format string, args ...any) error {
	return &NotEnoughTilesError{msg: fmt.Sprintf(format, args...)}
}

// String 返回牌的编码；越界值用 "Tile(N)" 表示，便于调试打印。
func (t Tile) String() string {
	if int(t) < NumTileKinds {
		return TileNames[t]
	}
	return "Tile(" + strconv.Itoa(int(t)) + ")"
}

// ParseTile 把牌面编码（如 "1m"、"9p"、"E"）解析成 Tile。
//
// 未知或格式非法的牌返回包装了 ErrInvalidHand 的错误。
func ParseTile(text string) (Tile, error) {
	if len(text) == 2 && text[0] >= '1' && text[0] <= '9' {
		base := -1
		switch text[1] {
		case 'm':
			base = 0
		case 'p':
			base = 9
		case 's':
			base = 18
		}
		if base >= 0 {
			return Tile(base + int(text[0]-'1')), nil
		}
	}
	switch text {
	case "E":
		return TileEast, nil
	case "S":
		return TileSouth, nil
	case "W":
		return TileWest, nil
	case "N":
		return TileNorth, nil
	case "P":
		return TileWhite, nil
	case "F":
		return TileGreen, nil
	case "C":
		return TileRed, nil
	}
	return 0, newInvalidHandError("未知牌：%q", text)
}

// ParseHand 把手牌简写解析成 Tile 切片。
//
// 简写规则与 Python 测试里的 手牌() 辅助函数一致：以 "m"/"p"/"s" 结尾的分组按序数牌
// 展开（"123m" → 1m 2m 3m），其余分组按字牌逐字符展开（"EE" → E E）。
// 例：ParseHand("123m 99s EE") → [1m 2m 3m 9s 9s E E]。
//
// 出现未知牌时返回包装了 ErrInvalidHand 的错误。
func ParseHand(text string) ([]Tile, error) {
	groups := strings.Fields(text)
	hand := make([]Tile, 0, len(text))
	for _, group := range groups {
		last := group[len(group)-1]
		if last == 'm' || last == 'p' || last == 's' {
			for i := 0; i < len(group)-1; i++ {
				tile, err := ParseTile(string([]byte{group[i], last}))
				if err != nil {
					return nil, err
				}
				hand = append(hand, tile)
			}
			continue
		}
		for i := 0; i < len(group); i++ {
			tile, err := ParseTile(group[i : i+1])
			if err != nil {
				return nil, err
			}
			hand = append(hand, tile)
		}
	}
	return hand, nil
}

// BuildDeck 构建一副完整的 136 张牌（34 种，每种 4 张），顺序与 TileNames 一致。
// 每次返回新切片，调用方可随意修改。
func BuildDeck() []Tile {
	deck := make([]Tile, 0, DeckSize)
	for tile := Tile(0); tile < NumTileKinds; tile++ {
		for i := 0; i < TilesPerKind; i++ {
			deck = append(deck, tile)
		}
	}
	return deck
}

// Shuffle 返回洗牌后的新牌墙，不修改入参；seed 相同则结果完全可复现。
//
// 随机源是 math/rand 的局部 Rand，不读全局随机状态，因此可并发调用。
// 注意：Go 的 PRNG 与洗牌实现和 Python 的 random.Random(seed) 不同，
// 跨语言不要期望相同 seed 得到同一副牌墙；本包只保证 Go 内部可复现。
func Shuffle(deck []Tile, seed int64) []Tile {
	shuffled := slices.Clone(deck)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})
	return shuffled
}

// DrawTile 从牌墙尾部摸一张牌，返回牌、剩余牌墙与是否摸到。
//
// 与 Python 版就地 pop 不同，这里不修改入参，而是把剩余牌墙作为返回值交还调用方；
// 调用方显式接住返回值即可，不会出现「谁持有同一个可变列表」的别名问题。
func DrawTile(wall []Tile) (tile Tile, rest []Tile, ok bool) {
	if len(wall) == 0 {
		return 0, wall, false
	}
	return wall[len(wall)-1], wall[:len(wall)-1], true
}

// ValidateHand 校验手牌合法性：张数（可选）、牌面、同种牌不超过 4 张。
//
// allowedLengths 省略表示不限制张数，只校验牌面与每种牌上限。
// 失败返回包装了 ErrInvalidHand 的错误；本函数不修改入参。
func ValidateHand(hand []Tile, allowedLengths ...int) error {
	if len(allowedLengths) > 0 && !slices.Contains(allowedLengths, len(hand)) {
		return newInvalidHandError("手牌张数必须是 %s，实际为 %d 张", joinInts(allowedLengths, " 或 "), len(hand))
	}
	var counts [NumTileKinds]uint8
	for _, tile := range hand {
		if int(tile) >= NumTileKinds {
			return newInvalidHandError("未知牌：%q", tile.String())
		}
		counts[tile]++
		if int(counts[tile]) > TilesPerKind {
			return newInvalidHandError("牌 %q 共有 %d 张，超过每种 %d 张的上限", tile.String(), counts[tile], TilesPerKind)
		}
	}
	return nil
}

// countValidated 把手牌计数进定长数组；调用前必须已通过 ValidateHand。
func countValidated(hand []Tile) [NumTileKinds]uint8 {
	var counts [NumTileKinds]uint8
	for _, tile := range hand {
		counts[tile]++
	}
	return counts
}

// SortHand 返回按 TileNames 顺序排序的新手牌，不修改入参。
//
// 因为 Tile 的数值就是固定顺序下标，直接数值排序即等价于 Python 里按 TILE_INDEX 排序。
// 非法手牌返回包装了 ErrInvalidHand 的错误。
func SortHand(hand []Tile) ([]Tile, error) {
	if err := ValidateHand(hand); err != nil {
		return nil, err
	}
	sorted := slices.Clone(hand)
	slices.Sort(sorted)
	return sorted, nil
}

// CountTiles 返回 34 长度的定长计数数组，下标即 Tile。
//
// 与 Python 版返回「只保留出现过的牌种」的 dict 不同：Go 这里固定返回 34 个 uint8，
// 调用方按需遍历即可，避免 map 的哈希与分配；未出现的牌计数为 0。
// 非法手牌返回包装了 ErrInvalidHand 的错误。
func CountTiles(hand []Tile) ([NumTileKinds]uint8, error) {
	if err := ValidateHand(hand); err != nil {
		return [NumTileKinds]uint8{}, err
	}
	return countValidated(hand), nil
}

// DealHands 发牌：每家 13 张，庄家（seats[0]，默认 east）多摸 1 张共 14 张。
//
// 按「每人一张、轮一圈」的顺序发牌，发完后庄家再摸第 14 张；返回座位到已排序手牌的
// 映射以及剩余牌墙（4 家标准局发完剩 136-53=83 张）。seats 为 nil 时使用 DefaultSeats。
//
// 与 Python 版就地消耗牌墙不同，这里不修改入参，剩余牌墙作为返回值交还，
// 调用方想继续摸牌就拿着它往下走。
//
// 座位为空 / 座位名重复返回包装了 ErrMahjong 的普通错误；
// 牌墙不足 13×家数+1 张返回包装了 ErrNotEnoughTiles 的错误。
func DealHands(wall []Tile, seats []string) (map[string][]Tile, []Tile, error) {
	if seats == nil {
		seats = DefaultSeats
	}
	if len(seats) == 0 {
		return nil, wall, fmt.Errorf("%w：座位列表不能为空", ErrMahjong)
	}
	seen := make(map[string]struct{}, len(seats))
	for _, seat := range seats {
		if _, ok := seen[seat]; ok {
			return nil, wall, fmt.Errorf("%w：座位名不能重复：%v", ErrMahjong, seats)
		}
		seen[seat] = struct{}{}
	}

	required := TenpaiHandSize*len(seats) + 1
	if len(wall) < required {
		return nil, wall, newNotEnoughTilesError("牌墙剩余 %d 张，发牌至少需要 %d 张", len(wall), required)
	}

	hands := make(map[string][]Tile, len(seats))
	rest := wall
	for round := 0; round < TenpaiHandSize; round++ {
		for _, seat := range seats {
			tile, next, _ := DrawTile(rest)
			rest = next
			hands[seat] = append(hands[seat], tile)
		}
	}
	tile, next, _ := DrawTile(rest)
	rest = next
	dealer := seats[0]
	hands[dealer] = append(hands[dealer], tile)

	for seat, hand := range hands {
		sorted, err := SortHand(hand)
		if err != nil {
			return nil, wall, err
		}
		hands[seat] = sorted
	}
	return hands, rest, nil
}

// joinInts 把整数切片拼成 "13 或 14" 形式的字符串。
func joinInts(values []int, sep string) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, sep)
}
