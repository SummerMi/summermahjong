package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/tile"
)

// seatCount 是座位数（契约 §2.1）。
const seatCount = 4

// jsonEncode 是统一 JSON 编码入口（HTTP 响应用）。
func jsonEncode(w io.Writer, v any) error {
	return json.NewEncoder(w).Encode(v)
}

// ── 服务端 → 客户端 ───────────────────────────────────────────────

// eventMessage 是协议 §6.2 的事件消息。
type eventMessage struct {
	Type    string         `json:"type"` // "event"
	Seq     uint64         `json:"seq"`
	Kind    string         `json:"kind"`
	Seat    int            `json:"seat"`
	Payload map[string]any `json:"payload,omitempty"`
}

// resumedMessage 是断线续传完成消息。
type resumedMessage struct {
	Type    string `json:"type"` // "resumed"
	FromSeq uint64 `json:"from_seq"`
	ToSeq   uint64 `json:"to_seq"`
}

// responsesMessage 是响应窗口推送给人类座位的消息（task-14 新增，契约 §6.2 扩展）。
//
// 客户端收到后应在 options 里选一个，用 {"type":"action", "seq":N, "action":{...}}
// 回传（与普通动作消息同格式）；超时未回则服务端按 pass 结算。
type responsesMessage struct {
	Type    string       `json:"type"` // "responses"
	Seq     uint64       `json:"seq"`  // 当前对局 seq（动作消息的 seq 回填这个值）
	Seat    int          `json:"seat"`
	Options []actionWire `json:"options"` // ron/pon/chi/kan/pass 候选项
}

// responseWindowEndMessage 表示响应窗口结束（pass/超时/结算都发一条），
// 前端可据此收起「等待响应」提示（task-14 新增）。
type responseWindowEndMessage struct {
	Type string `json:"type"` // "response_window_end"
	Seq  uint64 `json:"seq"`
}

// agentThoughtMessage 是 AI 决策的可观测信息（前端渲染用，task-14 新增）。
//
// 字段与前端约定一致：seat/agent/elapsed_ms/chosen/candidates。
type agentThoughtMessage struct {
	Type       string   `json:"type"` // "agent_thought"
	Seat       int      `json:"seat"`
	Agent      string   `json:"agent"`
	ElapsedMS  int64    `json:"elapsed_ms"`
	Chosen     string   `json:"chosen"`
	Candidates []string `json:"candidates"`
}

// pongMessage 是心跳响应，Ts 原样回显（用 RawMessage 保留客户端原始值）。
type pongMessage struct {
	Type string          `json:"type"` // "pong"
	Ts   json.RawMessage `json:"ts,omitempty"`
}

// errorMessage 是协议错误，连接不因此断开。
type errorMessage struct {
	Type    string `json:"type"` // "error"
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ── 客户端 → 服务端 ───────────────────────────────────────────────

// actionWire 是动作的线上表示（契约 §2.2 Action 的子集 + 文本牌面）。
type actionWire struct {
	Kind     string   `json:"kind"`
	Tile     string   `json:"tile,omitempty"`
	Consumed []string `json:"consumed,omitempty"`
}

// actionMessage 是客户端动作消息。
type actionMessage struct {
	Type     string     `json:"type"`
	Seq      uint64     `json:"seq,omitempty"`
	ActionID string     `json:"action_id,omitempty"`
	Action   actionWire `json:"action"`
}

// resumeMessage 是客户端断线续传消息。
type resumeMessage struct {
	Type    string `json:"type"`
	LastSeq uint64 `json:"last_seq"`
}

// pingMessage 是客户端心跳消息。
type pingMessage struct {
	Type string          `json:"type"`
	Ts   json.RawMessage `json:"ts,omitempty"`
}

// envelope 只解析消息类型，便于分发。
type envelope struct {
	Type string `json:"type"`
}

// newEventMessage 把事件转成线上消息。
func newEventMessage(ev game.Event) eventMessage {
	msg := eventMessage{
		Type: "event",
		Seq:  ev.Seq,
		Kind: string(ev.Kind),
		Seat: int(ev.Seat),
	}
	if len(ev.Payload) > 0 {
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			msg.Payload = payload
		}
	}
	return msg
}

// marshalEvent 把事件序列化为 JSON 字节（广播时只序列化一次）。
func marshalEvent(ev game.Event) ([]byte, error) {
	return json.Marshal(newEventMessage(ev))
}

// enrichEvent 按契约 §2.2 为事件补 payload（在 Apply 之后调用，用当前状态补公开信息）。
//
// 为什么要补：M1 的 game.Event 只带 Tile/TileRed 字段，契约要求 payload 结构；
// 富化后的事件会写进 store，所以回放 / 续传与实时广播三条路径拿到的事件完全一致。
func enrichEvent(s *game.State, ev *game.Event, tsumogiri bool) {
	switch ev.Kind {
	case game.EventRoundStart:
		ev.Payload = mustMarshal(map[string]any{
			"round_wind": int(s.RoundWind),
			"kyoku":      s.Kyoku,
			"honba":      s.Honba,
			"dealer":     int(s.Dealer),
			"scores":     s.Scores,
		})
	case game.EventDraw:
		ev.Payload = mustMarshal(map[string]any{
			"tile":           ev.Tile,
			"tile_red":       ev.TileRed,
			"wall_remaining": s.Wall.Remaining(),
		})
	case game.EventDiscard:
		ev.Payload = mustMarshal(map[string]any{
			"tile":            ev.Tile,
			"tile_red":        ev.TileRed,
			"tsumogiri":       tsumogiri,
			"riichi_declared": s.Riichi[ev.Seat],
		})
	}
}

// mustMarshal 序列化 map；对 map[string]any 的序列化不会失败。
func mustMarshal(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}

// parseAction 把线上动作转换为 game.Action。
//
// M1 只支持 draw / discard；其余动作在契约里已声明但引擎 M2 才实现，这里返回错误，
// 由调用方映射为 ILLEGAL_ACTION。
func parseAction(w actionWire) (game.Action, error) {
	kind, err := parseActionKind(w.Kind)
	if err != nil {
		return game.Action{}, err
	}
	action := game.Action{Kind: kind}
	if w.Tile != "" {
		t, err := tile.ParseTile(w.Tile)
		if err != nil {
			return game.Action{}, fmt.Errorf("%w: %v", game.ErrIllegalAction, err)
		}
		action.Tile = t
	}
	for _, text := range w.Consumed {
		t, err := tile.ParseTile(text)
		if err != nil {
			return game.Action{}, fmt.Errorf("%w: %v", game.ErrIllegalAction, err)
		}
		action.Consumed = append(action.Consumed, t)
	}
	return action, nil
}

// parseActionKind 解析动作名。
func parseActionKind(name string) (game.ActionKind, error) {
	switch name {
	case "draw":
		return game.ActDraw, nil
	case "discard":
		return game.ActDiscard, nil
	case "chi":
		return game.ActChi, nil
	case "pon":
		return game.ActPon, nil
	case "kan_daiminkan":
		return game.ActKanDaiminkan, nil
	case "kan_shouminkan":
		return game.ActKanShouminkan, nil
	case "kan_ankan":
		return game.ActKanAnkan, nil
	case "riichi":
		return game.ActRiichi, nil
	case "tsumo":
		return game.ActTsumo, nil
	case "ron":
		return game.ActRon, nil
	case "kyuushu":
		return game.ActKyuushu, nil
	case "pass":
		return game.ActPass, nil
	default:
		return 0, fmt.Errorf("%w: 未知动作 %q", game.ErrIllegalAction, name)
	}
}

// encodeAction 把 game.Action 转为线上动作。
func encodeAction(a game.Action) actionWire {
	out := actionWire{Kind: a.Kind.String()}
	if a.Kind == game.ActDiscard || a.Kind == game.ActTsumo || a.Kind == game.ActRon {
		out.Tile = a.Tile.String()
	}
	if len(a.Consumed) > 0 {
		out.Consumed = make([]string, 0, len(a.Consumed))
		for _, t := range a.Consumed {
			out.Consumed = append(out.Consumed, t.String())
		}
	}
	return out
}

// ── HTTP 响应体（契约 §6.1）────────────────────────────────────────

// meldWire 是副露的线上表示。
type meldWire struct {
	Kind  string   `json:"kind"`
	Tiles []string `json:"tiles"`
	From  int      `json:"from"`
}

// observeResponse 是 GET /api/v1/games/{id} 的响应：按座位裁剪后的 Observation。
type observeResponse struct {
	GameID         string                `json:"game_id"`
	Seq            uint64                `json:"seq"`
	Phase          string                `json:"phase"`
	Turn           int                   `json:"turn"`
	Seat           int                   `json:"seat"`
	WallRemaining  int                   `json:"wall_remaining"`
	Hand           []string              `json:"hand"`
	Drawn          *string               `json:"drawn,omitempty"`
	Melds          [seatCount][]meldWire `json:"melds"`
	Discards       [seatCount][]string   `json:"discards"`
	DoraIndicators []string              `json:"dora_indicators"`
	Scores         [seatCount]int        `json:"scores"`
	Riichi         [seatCount]bool       `json:"riichi"`
	RoundWind      int                   `json:"round_wind"`
	Kyoku          int                   `json:"kyoku"`
	Honba          int                   `json:"honba"`
	RiichiSticks   int                   `json:"riichi_sticks"`
	LegalActions   []actionWire          `json:"legal_actions"`
}

// createGameResponse 是建局响应。
type createGameResponse struct {
	GameID    string      `json:"game_id"`
	Seed      int64       `json:"seed"`
	Seq       uint64      `json:"seq"`
	Agents    []string    `json:"agents"`
	Ruleset   rulesetWire `json:"ruleset"`
	CreatedAt time.Time   `json:"created_at"`
}

// rulesetWire 是规则配置的线上表示。
type rulesetWire struct {
	RedFives       int `json:"red_fives"`
	StartingPoints int `json:"starting_points"`
}

// replayResponse 是回放响应：全量事件。
type replayResponse struct {
	GameID string         `json:"game_id"`
	Events []eventMessage `json:"events"`
	ToSeq  uint64         `json:"to_seq"`
}

// phaseName 返回阶段的文本名。
func phaseName(p game.Phase) string {
	switch p {
	case game.PhaseAwaitDraw:
		return "await_draw"
	case game.PhaseAwaitDiscard:
		return "await_discard"
	case game.PhaseAwaitResponse:
		return "await_response"
	case game.PhaseRoundEnd:
		return "round_end"
	case game.PhaseGameEnd:
		return "game_end"
	default:
		return "unknown"
	}
}

// toObserveResponse 把某个座位的 Observation 转为 HTTP 响应体。
func toObserveResponse(gameID string, seq uint64, phase game.Phase, turn game.Seat, obs game.Observation) observeResponse {
	resp := observeResponse{
		GameID:         gameID,
		Seq:            seq,
		Phase:          phaseName(phase),
		Turn:           int(turn),
		Seat:           int(obs.Seat),
		WallRemaining:  obs.WallRemaining,
		Hand:           tilesToStrings(obs.Hand),
		Melds:          [seatCount][]meldWire{},
		Discards:       [seatCount][]string{},
		DoraIndicators: tilesToStrings(obs.DoraIndicators),
		Scores:         obs.Scores,
		Riichi:         obs.Riichi,
		RoundWind:      int(obs.RoundWind),
		Kyoku:          obs.Kyoku,
		Honba:          obs.Honba,
		RiichiSticks:   obs.RiichiSticks,
		LegalActions:   make([]actionWire, 0, len(obs.LegalActions)),
	}
	if obs.Drawn != nil {
		text := obs.Drawn.String()
		resp.Drawn = &text
	}
	for i := 0; i < seatCount; i++ {
		resp.Melds[i] = meldsToWire(obs.Melds[i])
		resp.Discards[i] = tilesToStrings(obs.Discards[i])
	}
	for _, action := range obs.LegalActions {
		resp.LegalActions = append(resp.LegalActions, encodeAction(action))
	}
	return resp
}

// tilesToStrings 把牌切片转为文本切片（nil 保持 nil，JSON 输出 null）。
func tilesToStrings(tiles []tile.Tile) []string {
	if len(tiles) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(tiles))
	for _, t := range tiles {
		out = append(out, t.String())
	}
	return out
}

// meldsToWire 把副露切片转为线上表示。
func meldsToWire(melds []game.Meld) []meldWire {
	out := make([]meldWire, 0, len(melds))
	for _, m := range melds {
		out = append(out, meldWire{
			Kind:  string(m.Kind),
			Tiles: tilesToStrings(m.Tiles),
			From:  int(m.From),
		})
	}
	return out
}
