// Package game 实现日麻对局状态机（S5：摸打 + 副露 + 立直 + 振听 + 响应窗口 + 和了/流局 + 局/场流程）。
//
// 设计原则：
//   - 状态机是纯逻辑：不碰 I/O，不依赖真实时间/全局随机（随机只来自 tile.Wall 的 seed）。
//   - 每个动作产生事件，state = fold(events)；同 seed + 同动作序列 ⇒ 同状态（Replay 可复现）。
//   - 规则语义按 docs/riichi-接口契约-20261110.md（v1.2）与 docs/riichi-规则范围与役种清单。
//   - 和了计分通过 ScoreProvider 注入：S4 合并前用占位实现（1 翻 30 符），
//     合并后注入 rules.Evaluate/Score 的适配器即可，引擎流程不用改。
//
// 两套响应策略（Options.ManualResponses）：
//   - false（默认，兼容 S6/旧回放）：打出牌后把所有可选响应视为 pass，立即结算并轮到下家；
//     观感与 M1 完全一致，因此 S6 的 30 个测试与旧 game 测试不需要改动。
//   - true（S5 真打）：进入 PhaseAwaitResponse，必须由各家显式响应（ron/pon/kan/chi/pass），
//     或调用 ResolveTimeout() 模拟超时；响应通过 State.Responses(seat) 查询。
package game

import (
	"encoding/json"
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
	PhaseAwaitDiscard  Phase = iota // 已摸牌，等待打牌/立直/暗杠/加杠/自摸
	PhaseAwaitDraw                  // 等待摸牌
	PhaseRoundEnd                   // 本局结束，等待 StartNextRound（结算已写入事件与点数）
	PhaseGameEnd                    // 整场结束（南 4 结束 / トビ終了）
	PhaseAwaitResponse              // 打出牌/加杠后等待四家响应（S5 新增）
)

// String 返回阶段文本（便于日志与测试断言）。
func (p Phase) String() string {
	switch p {
	case PhaseAwaitDiscard:
		return "await_discard"
	case PhaseAwaitDraw:
		return "await_draw"
	case PhaseRoundEnd:
		return "round_end"
	case PhaseGameEnd:
		return "game_end"
	case PhaseAwaitResponse:
		return "await_response"
	default:
		return "unknown"
	}
}

// ActionKind 是动作类型。
type ActionKind uint8

const (
	ActDraw ActionKind = iota
	ActDiscard
	ActChi
	ActPon
	ActKanDaiminkan
	ActKanShouminkan
	ActKanAnkan
	ActRiichi
	ActTsumo
	ActRon
	ActKyuushu
	ActPass
)

// String 返回动作名。
func (k ActionKind) String() string {
	switch k {
	case ActDraw:
		return "draw"
	case ActDiscard:
		return "discard"
	case ActChi:
		return "chi"
	case ActPon:
		return "pon"
	case ActKanDaiminkan:
		return "kan_daiminkan"
	case ActKanShouminkan:
		return "kan_shouminkan"
	case ActKanAnkan:
		return "kan_ankan"
	case ActRiichi:
		return "riichi"
	case ActTsumo:
		return "tsumo"
	case ActRon:
		return "ron"
	case ActKyuushu:
		return "kyuushu"
	case ActPass:
		return "pass"
	default:
		return "?"
	}
}

// Action 是一个动作。
type Action struct {
	Kind ActionKind
	Seat Seat
	Tile tile.Tile // 打牌/和了牌/被副露的牌/暗杠的牌种

	// Consumed 是副露消耗的手牌（吃/碰 2 张、明杠/暗杠 3 张；加杠为空）。
	Consumed []tile.Tile
	// Options 预留给后续规则（立直宣言、杠种类等）。
	Options map[string]any
}

// EventKind 是事件类型（契约 §2.2 冻结全集）。
type EventKind string

const (
	EventRoundStart EventKind = "round_start"
	EventDraw       EventKind = "draw"
	EventDiscard    EventKind = "discard"
	EventChi        EventKind = "chi"
	EventPon        EventKind = "pon"
	EventKan        EventKind = "kan"
	EventRiichi     EventKind = "riichi"
	EventDoraReveal EventKind = "dora_reveal"
	EventTsumo      EventKind = "tsumo"
	EventRon        EventKind = "ron"
	EventRyuukyoku  EventKind = "ryuukyoku"
	EventRoundEnd   EventKind = "round_end"
	EventGameEnd    EventKind = "game_end"
	EventError      EventKind = "error"
)

// Event 是一条不可变事件。
type Event struct {
	Seq     uint64          `json:"seq"`
	Kind    EventKind       `json:"kind"`
	Seat    Seat            `json:"seat"`
	Tile    string          `json:"tile,omitempty"`
	TileRed bool            `json:"tile_red,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Ruleset 是对局规则配置（契约 §8 的子集 + S5 需要的开关）。
type Ruleset struct {
	RedFives       int  // 赤宝牌数量 0-3
	StartingPoints int  // 起始点数，默认 25000
	DoubleRon      bool // 多家荣和（双响/三响）是否都成立；false 时按头跳只算最近一家
	RoundLimit     int  // 半荘局数上限（默认 8：东 1-4 + 南 1-4）
}

// DefaultRuleset 返回天鳳般默认配置。
func DefaultRuleset() Ruleset {
	return Ruleset{
		RedFives:       3,
		StartingPoints: 25000,
		DoubleRon:      true,
		RoundLimit:     8,
	}
}

// maxRoundWind 是半荘的场风上限（南 4 结束）。
const maxRoundWind = South

// maxKyokuInWind 是每个场风的局数（东 1-4）。
const maxKyokuInWind = 4

// State 是一局对局的状态。
type State struct {
	Rules Ruleset

	Seed       int64
	RoundWind  Wind
	Kyoku      int // 第几局（东 1 局 = 1）
	Honba      int // 本场
	RiichiStix int // 场上立直棒

	Dealer Seat
	Turn   Seat
	Phase  Phase

	Wall     *tile.Wall
	Hands    [4][]tile.Tile
	Melds    [4][]Meld
	Discards [4][]tile.Tile
	Scores   [4]int
	Drawn    *tile.Tile
	Riichi   [4]bool
	Furiten  [4]bool

	// ── S5 新增（兼容期新增字段，契约允许）──
	TempFuriten      [4]bool           // 同巡振听：本巡内放弃荣和后，直到自己下次摸牌前不能荣和
	Ippatsu          [4]bool           // 一发：立直后一巡内（无副露打断）和了成立
	Pending          *PendingResponses // 非 nil 表示处于响应窗口
	DrawCount        int               // 本局累计摸牌次数
	SeatDraws        [4]int            // 各家摸牌次数（清临时振听 / 一发用）
	Kuikae           [4][]tile.Kind    // 刚吃/碰后立即打牌的禁则牌种（食替禁止）
	LastDealerRepeat bool              // 上一局结束时是否连庄（StartNextRound 使用）
	Calls            int               // 本局副露/杠次数（九种九牌与一发判定用）
	lastDrawRinshan  bool              // 上一张摸的牌是否来自岭上（岭上开花判定用）

	Seq    uint64
	Events []Event

	opts Options
}

// ErrIllegalAction 表示动作不合法。
var ErrIllegalAction = errors.New("game: illegal action")

// errRoundNotEnded 表示还没到可以开下一局的状态。
var errRoundNotEnded = errors.New("game: round not ended")

// New 创建对局（契约签名，使用默认选项：自动 pass 响应，兼容既有调用方）。
func New(seed int64, rules Ruleset) (*State, error) {
	return NewWithOptions(seed, rules, Options{})
}

// NewWithOptions 创建对局：洗牌、配牌、决定起家；opts 控制响应策略与计分实现。
func NewWithOptions(seed int64, rules Ruleset, opts Options) (*State, error) {
	if rules.StartingPoints <= 0 {
		rules.StartingPoints = DefaultRuleset().StartingPoints
	}
	if rules.RoundLimit <= 0 {
		rules.RoundLimit = DefaultRuleset().RoundLimit
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
		Kyoku:     1,
		Wall:      wall,
		Hands:     hands,
		Turn:      0,
		Dealer:    0,
		Phase:     PhaseAwaitDraw,
		opts:      opts.withDefaults(),
	}
	for i := range s.Scores {
		s.Scores[i] = rules.StartingPoints
	}
	s.emitRoundStart()
	return s, nil
}

// Options 是状态机的可选配置。
type Options struct {
	// ManualResponses 为 true 时，打出牌后停在 PhaseAwaitResponse 等各家显式响应；
	// 默认 false：自动视为全部 pass，行为与 M1/S6 现状一致（S6 尚未实现响应交互）。
	ManualResponses bool
	// ScoreProvider 是计分实现；nil 时使用占位实现（1 翻 30 符）。
	ScoreProvider ScoreProvider
}

// withDefaults 补齐零值选项：默认使用真实算点（rules.Evaluate/Score），
// 需要固定 1 翻 30 符的旧行为时显式注入 placeholderScorer（测试用）。
func (o Options) withDefaults() Options {
	if o.ScoreProvider == nil {
		o.ScoreProvider = RulesScorer{}
	}
	return o
}

// CurrentSeat 返回当前该行动的座位。
func (s *State) CurrentSeat() Seat { return s.Turn }

// CurrentWind 返回某座位的自风。
func (s *State) CurrentWind(seat Seat) Wind {
	return Wind((int(seat) - int(s.Dealer) + 4) % 4)
}

// NextSeat 返回某座位的下家。
func NextSeat(seat Seat) Seat { return Seat((int(seat) + 1) % 4) }

// meldCount 返回某座位已副露的面子数（杠按 1 组计）。
func (s *State) meldCount(seat Seat) int { return len(s.Melds[seat]) }

// emit 追加事件并递增序号。
func (s *State) emit(e Event) {
	s.Seq++
	e.Seq = s.Seq
	s.Events = append(s.Events, e)
}

// emitPayload 追加带 payload 的事件。
func (s *State) emitPayload(kind EventKind, seat Seat, tl *tile.Tile, payload any) {
	ev := Event{Kind: kind, Seat: seat}
	if tl != nil {
		ev.Tile = tl.String()
		ev.TileRed = tl.Aka
	}
	if payload != nil {
		ev.Payload = mustJSON(payload)
	}
	s.emit(ev)
}

// mustJSON 序列化 payload；失败时返回 nil（不 panic，事件仍保留）。
func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// TotalTiles 统计当前状态下的总牌数（手牌 + 副露 + 舍牌 + 活牌剩余 + 王牌剩余）。
//
// 不变量：任何时刻都应等于 136。杠后岭上牌从王牌摸走、副露把牌从手牌移到 Melds、
// 鸣牌把牌从牌河拿走，这里逐项计入，保证「换存储不换行为」的属性测试仍然成立。
func (s *State) TotalTiles() int {
	deadRemaining := tile.DeadWallSize - s.Wall.KanCount()
	n := s.Wall.Remaining() + deadRemaining
	for seat := 0; seat < 4; seat++ {
		n += len(s.Hands[seat]) + len(s.Discards[seat])
		for _, meld := range s.Melds[seat] {
			n += len(meld.Tiles)
		}
	}
	return n
}

// GameOver 报告整场是否已结束。
func (s *State) GameOver() bool { return s.Phase == PhaseGameEnd }

// Apply 执行一个动作，推进状态并追加事件。
//
// 签名是契约（不能改）：响应动作（ron/pon/kan/chi/pass）也走 Apply，
// 由阶段与 PendingResponses 决定归属，而不是看 a.Seat 是否等于 Turn。
func (s *State) Apply(a Action) error {
	switch s.Phase {
	case PhaseAwaitDraw:
		return s.applyDraw(a)
	case PhaseAwaitDiscard:
		return s.applyDiscardPhase(a)
	case PhaseAwaitResponse:
		return s.applyResponse(a)
	case PhaseRoundEnd:
		return fmt.Errorf("%w: 本局已结束，请调用 StartNextRound", ErrIllegalAction)
	case PhaseGameEnd:
		return fmt.Errorf("%w: 整场已结束", ErrIllegalAction)
	default:
		return fmt.Errorf("%w: 未知阶段 %v", ErrIllegalAction, s.Phase)
	}
}

// Legal 返回当前阶段「当前行动者」可执行的合法动作。
//
// 响应窗口期间返回 nil：响应选项与行动者无关，用 Responses(seat) 查询。
func (s *State) Legal() []Action {
	switch s.Phase {
	case PhaseAwaitDraw:
		return []Action{{Kind: ActDraw, Seat: s.Turn}}
	case PhaseAwaitDiscard:
		return s.legalDiscardActions(s.Turn)
	default:
		return nil
	}
}

// Responses 返回某座位在当前响应窗口的合法响应动作（含 pass）；没有窗口时返回 nil。
func (s *State) Responses(seat Seat) []Action {
	if s.Phase != PhaseAwaitResponse || s.Pending == nil {
		return nil
	}
	if s.Pending.Decided[seat] {
		return nil
	}
	out := make([]Action, 0, len(s.Pending.Options[seat])+1)
	out = append(out, s.Pending.Options[seat]...)
	out = append(out, Action{Kind: ActPass, Seat: seat})
	return out
}

// Replay 用 seed + 动作序列重建状态（默认选项）。
func Replay(seed int64, rules Ruleset, actions []Action) (*State, error) {
	return ReplayWithOptions(seed, rules, actions, Options{})
}

// ReplayWithOptions 用指定选项回放动作序列。
//
// 与契约函数 Replay 的差异（签名不变，仅新增这个变体）：
//   - Replay 等价于 ReplayWithOptions(..., Options{})，即「自动 pass 响应」模式，
//     只能回放不含显式响应动作（ron/pon/chi/kan/pass）的序列；
//   - 手动响应模式（Options{ManualResponses:true}）产生的动作序列里包含响应动作，
//     必须用 ReplayWithOptions 并传同样的 Options，否则阶段不符会报错。
//
// 若动作序列中间出现「本局结束」，会先调用 StartNextRound 再继续应用后续动作
// （与实时对局调用方的驱动顺序一致），因此回放可以跨局复现。
func ReplayWithOptions(seed int64, rules Ruleset, actions []Action, opts Options) (*State, error) {
	s, err := NewWithOptions(seed, rules, opts)
	if err != nil {
		return nil, err
	}
	for i, a := range actions {
		// 实时驱动在「空牌山」处会尝试摸牌并荒牌流局（该动作因返回错误不进入动作序列）。
		// 回放在应用下一个动作前补跑这次失败的摸牌，再自动开下一局，从而与实时驱动一致。
		if s.Phase == PhaseAwaitDraw && s.Wall.Remaining() == 0 {
			_ = s.applyDraw(Action{Kind: ActDraw, Seat: s.Turn})
		}
		if s.Phase == PhaseRoundEnd {
			if err := s.StartNextRound(); err != nil {
				return nil, fmt.Errorf("replay start next round before action %d: %w", i, err)
			}
		}
		if err := s.Apply(a); err != nil {
			return nil, fmt.Errorf("replay action %d: %w", i, err)
		}
	}
	// 实时对局在「摸到牌山空」的那一次摸牌会荒牌流局（该动作因返回错误不会被记录进
	// 动作序列）。回放动作序列已全部应用后，若正停在空牌山的摸牌点，补跑这次失败的摸牌，
	// 让回放终态与实时终态（含 ryuukyoku / round_end 事件）一致。
	if s.Phase == PhaseAwaitDraw && s.Wall.Remaining() == 0 {
		_ = s.applyDraw(Action{Kind: ActDraw, Seat: s.Turn})
	}
	return s, nil
}
