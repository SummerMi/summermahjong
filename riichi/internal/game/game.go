// Package game 实现日麻对局状态机（M1 阶段：配牌、摸打、轮转、事件日志）。
//
// 设计原则：
//   - 状态机是纯逻辑：不碰 I/O，不依赖时间/随机（随机只来自 tile.Wall 的 seed）。
//   - 每个动作产生一条 Event，state = fold(events)；同 seed + 同动作序列 ⇒ 同状态。
//   - 副露/立直/和了/流局在 M1-M2 逐步补齐，接口保持不变。
package game

import (
	"errors"
	"fmt"

	"summermahjong/riichi/internal/tile"
)

// Seat 是座位号 0-3（0 为起家，按逆时针）。
type Seat uint8

// String 返回座位文本。
func (s Seat) String() string { return fmt.Sprintf("seat%d", s) }

// Wind 是风位：东 0、南 1、西 2、北 3。
type Wind uint8

const (
	East Wind = iota
	South
	West
	North
)

// String 返回风位文本。
func (w Wind) String() string {
	switch w {
	case East:
		return "东"
	case South:
		return "南"
	case West:
		return "西"
	case North:
		return "北"
	default:
		return "?"
	}
}

// Phase 是对局阶段。
type Phase uint8

const (
	PhaseAwaitDiscard Phase = iota // 已摸牌，等待打牌
	PhaseAwaitDraw                 // 等待摸牌
	PhaseRoundEnd                  // 本局结束（M1 暂不产生）
	PhaseGameEnd                   // 整场结束（M1 暂不产生）
)

// ActionKind 是动作类型。
type ActionKind uint8

const (
	ActDraw ActionKind = iota
	ActDiscard
)

// String 返回动作名。
func (k ActionKind) String() string {
	switch k {
	case ActDraw:
		return "draw"
	case ActDiscard:
		return "discard"
	default:
		return "?"
	}
}

// Action 是一个动作。
type Action struct {
	Kind ActionKind
	Seat Seat
	Tile tile.Tile // 仅 Discard 使用
}

// EventKind 是事件类型。
type EventKind string

// 事件类型（M1 只有三种）。
const (
	EventRoundStart EventKind = "round_start"
	EventDraw       EventKind = "draw"
	EventDiscard    EventKind = "discard"
)

// Event 是一条不可变事件。
type Event struct {
	Seq     uint64    `json:"seq"`
	Kind    EventKind `json:"kind"`
	Seat    Seat      `json:"seat"`
	Tile    string    `json:"tile,omitempty"`
	TileRed bool      `json:"tile_red,omitempty"`
}

// Ruleset 是对局规则配置。
type Ruleset struct {
	RedFives       int // 赤宝牌数量 0-3
	StartingPoints int // 起始点数，默认 25000
}

// DefaultRuleset 返回天鳳般默认配置。
func DefaultRuleset() Ruleset {
	return Ruleset{RedFives: 3, StartingPoints: 25000}
}

// State 是一局对局的状态。
type State struct {
	Rules Ruleset

	Seed       int64
	RoundWind  Wind
	Honba      int // 本场
	RiichiStix int // 场上立直棒

	Dealer Seat
	Turn   Seat
	Phase  Phase

	Wall     *tile.Wall
	Hands    [4][]tile.Tile
	Discards [4][]tile.Tile
	Scores   [4]int
	Drawn    *tile.Tile

	Seq    uint64
	Events []Event
}

// ErrIllegalAction 表示动作不合法。
var ErrIllegalAction = errors.New("game: illegal action")

// New 创建对局：洗牌、配牌、决定起家。
func New(seed int64, rules Ruleset) (*State, error) {
	if rules.StartingPoints <= 0 {
		rules.StartingPoints = DefaultRuleset().StartingPoints
	}
	wall, err := tile.NewWall(seed, rules.RedFives)
	if err != nil {
		return nil, err
	}
	hands, err := wall.Deal()
	if err != nil {
		return nil, err
	}
	s := &State{
		Rules:     rules,
		Seed:      seed,
		RoundWind: East,
		Wall:      wall,
		Hands:     hands,
		Turn:      0,
		Dealer:    0,
		Phase:     PhaseAwaitDraw,
	}
	for i := range s.Scores {
		s.Scores[i] = rules.StartingPoints
	}
	s.emit(Event{Kind: EventRoundStart, Seat: 0})
	return s, nil
}

// CurrentSeat 返回当前该行动的座位。
func (s *State) CurrentSeat() Seat { return s.Turn }

// Legal 返回当前座位可执行的合法动作（M1：摸或打）。
func (s *State) Legal() []Action {
	switch s.Phase {
	case PhaseAwaitDraw:
		return []Action{{Kind: ActDraw, Seat: s.Turn}}
	case PhaseAwaitDiscard:
		acts := make([]Action, 0, len(s.Hands[s.Turn]))
		for _, t := range s.Hands[s.Turn] {
			acts = append(acts, Action{Kind: ActDiscard, Seat: s.Turn, Tile: t})
		}
		return acts
	default:
		return nil
	}
}

// Apply 执行一个动作，推进状态并追加事件。
func (s *State) Apply(a Action) error {
	if a.Seat != s.Turn {
		return fmt.Errorf("%w: seat %d is not current seat %d", ErrIllegalAction, a.Seat, s.Turn)
	}
	switch a.Kind {
	case ActDraw:
		if s.Phase != PhaseAwaitDraw {
			return fmt.Errorf("%w: cannot draw in phase %v", ErrIllegalAction, s.Phase)
		}
		t, ok := s.Wall.Draw()
		if !ok {
			s.Phase = PhaseRoundEnd
			return fmt.Errorf("%w: wall exhausted", ErrIllegalAction)
		}
		s.Drawn = &t
		s.Hands[s.Turn] = append(s.Hands[s.Turn], t)
		s.Phase = PhaseAwaitDiscard
		s.emit(Event{Kind: EventDraw, Seat: s.Turn, Tile: t.String(), TileRed: t.Red})
		return nil

	case ActDiscard:
		if s.Phase != PhaseAwaitDiscard {
			return fmt.Errorf("%w: cannot discard in phase %v", ErrIllegalAction, s.Phase)
		}
		idx := -1
		for i, t := range s.Hands[s.Turn] {
			if t == a.Tile {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("%w: tile %v not in hand", ErrIllegalAction, a.Tile)
		}
		hand := s.Hands[s.Turn]
		s.Hands[s.Turn] = append(hand[:idx:idx], hand[idx+1:]...)
		s.Discards[s.Turn] = append(s.Discards[s.Turn], a.Tile)
		s.Drawn = nil
		s.emit(Event{Kind: EventDiscard, Seat: s.Turn, Tile: a.Tile.String(), TileRed: a.Tile.Red})
		s.Turn = (s.Turn + 1) % 4
		s.Phase = PhaseAwaitDraw
		return nil

	default:
		return fmt.Errorf("%w: unknown action kind %v", ErrIllegalAction, a.Kind)
	}
}

// emit 追加事件并递增序号。
func (s *State) emit(e Event) {
	s.Seq++
	e.Seq = s.Seq
	s.Events = append(s.Events, e)
}

// TotalTiles 统计当前状态下的总牌数（手牌 + 舍牌 + 已摸 + 活牌剩余 + 王牌）。
func (s *State) TotalTiles() int {
	n := s.Wall.Remaining() + tile.DeadWallSize
	for seat := 0; seat < 4; seat++ {
		n += len(s.Hands[seat]) + len(s.Discards[seat])
	}
	return n
}

// Replay 用 seed + 动作序列重建状态，用于回放一致性与评测复现。
func Replay(seed int64, rules Ruleset, actions []Action) (*State, error) {
	s, err := New(seed, rules)
	if err != nil {
		return nil, err
	}
	for i, a := range actions {
		if err := s.Apply(a); err != nil {
			return nil, fmt.Errorf("replay action %d: %w", i, err)
		}
	}
	return s, nil
}
