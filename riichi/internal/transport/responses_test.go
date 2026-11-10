package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/rules"
	"summermahjong/riichi/internal/store"
	"summermahjong/riichi/internal/tile"
)

// ── 构造可控房间（响应窗口测试用）──────────────────────────────────

// craftHand 解析手牌文本。
func craftHand(t *testing.T, text string) []tile.Tile {
	t.Helper()
	hand, err := tile.ParseHand(text)
	if err != nil {
		t.Fatalf("解析手牌 %q: %v", text, err)
	}
	return hand
}

// craftTile 解析单张牌。
func craftTile(t *testing.T, text string) tile.Tile {
	t.Helper()
	tl, err := tile.ParseTile(text)
	if err != nil {
		t.Fatalf("解析牌 %q: %v", text, err)
	}
	return tl
}

// craftState 构造「庄家 seat0 已摸牌、等待打牌」的手动响应局面。
func craftState(t *testing.T, seat0Hand, h1, h2, h3 string) *game.State {
	t.Helper()
	st, err := game.NewWithOptions(20261110, game.DefaultRuleset(), game.Options{
		ManualResponses: true,
		ScoreProvider:   game.RulesScorer{},
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	texts := [4]string{seat0Hand, h1, h2, h3}
	for seat := 0; seat < 4; seat++ {
		st.Hands[seat] = craftHand(t, texts[seat])
		st.Melds[seat] = nil
		st.Discards[seat] = nil
	}
	if len(st.Hands[0]) != 14 {
		t.Fatalf("seat0 手牌应为 14 张，实际 %d", len(st.Hands[0]))
	}
	for seat := 1; seat < 4; seat++ {
		if len(st.Hands[seat]) != 13 {
			t.Fatalf("seat%d 手牌应为 13 张，实际 %d", seat, len(st.Hands[seat]))
		}
	}
	drawn := st.Hands[0][13]
	st.Drawn = &drawn
	st.Phase = game.PhaseAwaitDiscard
	st.Turn = 0
	st.DrawCount = 1
	st.SeatDraws = [4]int{1, 0, 0, 0}
	st.Scores = [4]int{25000, 25000, 25000, 25000}
	return st
}

// craftRoom 用给定状态建房间并挂上四个座位的内存连接。
func craftRoom(t *testing.T, st *game.State, cfg roomConfig) (*Room, [4]*client) {
	t.Helper()
	if cfg.store == nil {
		cfg.store = newSyncStore(store.NewMemory())
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	cfg.manualResponses = true
	meta := store.GameMeta{ID: "crafted-room"}
	r := newRoom("crafted-room", st, meta, cfg)
	if err := cfg.store.CreateGame(context.Background(), meta); err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	if err := r.persistInitial(context.Background()); err != nil {
		t.Fatalf("persistInitial: %v", err)
	}
	var clients [4]*client
	for seat := 0; seat < 4; seat++ {
		c := newClient(game.Seat(seat))
		c.synced = true
		r.subsMu.Lock()
		r.subs[c] = struct{}{}
		r.subsMu.Unlock()
		clients[seat] = c
	}
	t.Cleanup(r.Close)
	return r, clients
}

// readResponses 从连接发送缓冲读一条 responses 消息（跳过普通事件）。
func readResponses(t *testing.T, c *client) responsesMessage {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case data := <-c.send:
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("解析消息: %v (%s)", err, data)
			}
			if envelope.Type != "responses" {
				continue
			}
			var msg responsesMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("解析 responses 消息: %v (%s)", err, data)
			}
			return msg
		case <-deadline:
			t.Fatal("等待 responses 消息超时")
		}
	}
}

// readRoomEvents 从连接发送缓冲读 n 条 event 消息（跳过新协议消息）。
func readRoomEvents(t *testing.T, c *client, n int) []eventMessage {
	t.Helper()
	out := make([]eventMessage, 0, n)
	deadline := time.After(2 * time.Second)
	for len(out) < n {
		select {
		case data := <-c.send:
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("解析消息: %v (%s)", err, data)
			}
			switch envelope.Type {
			case "event":
				var msg eventMessage
				if err := json.Unmarshal(data, &msg); err != nil {
					t.Fatalf("解析 event: %v", err)
				}
				out = append(out, msg)
			case "responses", "response_window_end", "agent_thought":
				continue
			default:
				t.Fatalf("期望 event，收到 type=%s", envelope.Type)
			}
		case <-deadline:
			t.Fatalf("等待 event 超时（已收 %d/%d）", len(out), n)
		}
	}
	return out
}

// drainClientTypes 排空连接发送缓冲，统计出现过的消息类型。
func drainClientTypes(t *testing.T, c *client) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for {
		select {
		case data := <-c.send:
			var envelope struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatalf("解析消息类型: %v (%s)", err, data)
			}
			seen[envelope.Type]++
		default:
			return seen
		}
	}
}

// submitHuman 直接调用房间 actor 的 handleAction（同步版，便于确定性测试）。
func submitHuman(t *testing.T, r *Room, seat game.Seat, action game.Action) error {
	t.Helper()
	reply := make(chan error, 1)
	r.handleAction(&actionRequest{seat: seat, action: action, reply: reply})
	return <-reply
}

// pendingKinds 返回 responses 消息里的动作类型集合。
func pendingKinds(msg responsesMessage) map[string]bool {
	out := map[string]bool{}
	for _, option := range msg.Options {
		out[option.Kind] = true
	}
	return out
}

// countRoomEvent 统计房间状态里某类事件数量（测试同步驱动时安全）。
func countRoomEvent(r *Room, kind game.EventKind) int {
	n := 0
	for _, ev := range r.st.Events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// firstResponseAction 从当前待响应选项里找指定类型（chi 需要带消耗牌）。
func firstResponseAction(t *testing.T, st *game.State, seat game.Seat, kind game.ActionKind) game.Action {
	t.Helper()
	for _, action := range st.Responses(seat) {
		if action.Kind == kind {
			return action
		}
	}
	t.Fatalf("seat%d 没有 %v 响应选项: %v", seat, kind, st.Responses(seat))
	return game.Action{}
}

// ── 响应窗口用例（7 条：ron/pon/chi/pass/超时/多人抢/AI）────────

// TestResponseWindowRonBeatsPon 荣和优先于碰：推送给 seat1(ron)/seat2(pon)，ron 成立。
func TestResponseWindowRonBeatsPon(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p",   // seat1 荣和 5p
		"5p5p123m456m789m1p2p", // seat2 可碰
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	r, clients := craftRoom(t, st, roomConfig{})

	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	if r.st.Phase != game.PhaseAwaitResponse {
		t.Fatalf("打牌后 phase=%v want await_response", r.st.Phase)
	}
	seat1Msg := readResponses(t, clients[1])
	seat2Msg := readResponses(t, clients[2])
	if !pendingKinds(seat1Msg)["ron"] {
		t.Fatalf("seat1 应有 ron 选项: %v", seat1Msg.Options)
	}
	if !pendingKinds(seat2Msg)["pon"] {
		t.Fatalf("seat2 应有 pon 选项: %v", seat2Msg.Options)
	}
	if got := seat1Msg.Seq; got != r.st.Seq {
		t.Fatalf("responses.seq=%d want %d", got, r.st.Seq)
	}

	if err := submitHuman(t, r, 1, game.Action{Kind: game.ActRon, Seat: 1, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("seat1 ron 失败: %v", err)
	}
	if err := submitHuman(t, r, 2, game.Action{Kind: game.ActPon, Seat: 2, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("seat2 pon 失败: %v", err)
	}
	if r.st.Phase != game.PhaseRoundEnd {
		t.Fatalf("phase=%v want round_end", r.st.Phase)
	}
	if countRoomEvent(r, game.EventRon) != 1 {
		t.Fatalf("ron 事件数=%d want 1", countRoomEvent(r, game.EventRon))
	}
	if countRoomEvent(r, game.EventPon) != 0 {
		t.Fatal("荣和优先时碰不应成立")
	}
	if r.st.Scores[1] != 28900 || r.st.Scores[0] != 21100 {
		t.Fatalf("scores=%v", r.st.Scores)
	}
}

// TestResponseWindowPonBeatsChi 碰优先于吃：seat1 吃、seat2 碰，碰成立。
func TestResponseWindowPonBeatsChi(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p",  // seat1 可吃 5p
		"5p5p123m456m789m1p2p", // seat2 可碰
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	r, clients := craftRoom(t, st, roomConfig{})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	seat1Msg := readResponses(t, clients[1])
	seat2Msg := readResponses(t, clients[2])
	if !pendingKinds(seat1Msg)["chi"] || !pendingKinds(seat2Msg)["pon"] {
		t.Fatalf("选项缺失: seat1=%v seat2=%v", seat1Msg.Options, seat2Msg.Options)
	}
	if err := submitHuman(t, r, 1, firstResponseAction(t, r.st, 1, game.ActChi)); err != nil {
		t.Fatalf("seat1 吃失败: %v", err)
	}
	if err := submitHuman(t, r, 2, game.Action{Kind: game.ActPon, Seat: 2, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("seat2 碰失败: %v", err)
	}
	if countRoomEvent(r, game.EventPon) != 1 || countRoomEvent(r, game.EventChi) != 0 {
		t.Fatalf("事件统计 pon=%d chi=%d", countRoomEvent(r, game.EventPon), countRoomEvent(r, game.EventChi))
	}
	if len(r.st.Melds[2]) != 1 || r.st.Melds[2][0].Kind != game.MeldPon {
		t.Fatalf("seat2 副露=%v", r.st.Melds[2])
	}
	if r.st.Turn != 2 || r.st.Phase != game.PhaseAwaitDiscard {
		t.Fatalf("碰后 turn/phase=%d/%v", r.st.Turn, r.st.Phase)
	}
}

// TestResponseWindowChi 只有下家能吃：吃成立并轮转到吃的人。
func TestResponseWindowChi(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"3p4p123m456m789m11p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	r, clients := craftRoom(t, st, roomConfig{})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	msg := readResponses(t, clients[1])
	if !pendingKinds(msg)["chi"] {
		t.Fatalf("seat1 应有 chi 选项: %v", msg.Options)
	}
	if err := submitHuman(t, r, 1, firstResponseAction(t, r.st, 1, game.ActChi)); err != nil {
		t.Fatalf("吃失败: %v", err)
	}
	if countRoomEvent(r, game.EventChi) != 1 {
		t.Fatalf("chi 事件数=%d want 1", countRoomEvent(r, game.EventChi))
	}
	if r.st.Turn != 1 || r.st.Phase != game.PhaseAwaitDiscard {
		t.Fatalf("吃后 turn/phase=%d/%v", r.st.Turn, r.st.Phase)
	}
	if len(r.st.Melds[1]) != 1 || r.st.Melds[1][0].Kind != game.MeldChi {
		t.Fatalf("seat1 副露=%v", r.st.Melds[1])
	}
}

// TestResponseWindowPassAll 全部 pass：进入下家摸牌阶段并广播 response_window_end。
func TestResponseWindowPassAll(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 可荣和
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	r, clients := craftRoom(t, st, roomConfig{})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	_ = readResponses(t, clients[1])
	if err := submitHuman(t, r, 1, game.Action{Kind: game.ActPass, Seat: 1}); err != nil {
		t.Fatalf("seat1 pass 失败: %v", err)
	}
	// room 会自动替 seat1 摸牌，所以这里停在 seat1 的摸牌/打牌阶段
	if r.st.Turn != 1 || (r.st.Phase != game.PhaseAwaitDraw && r.st.Phase != game.PhaseAwaitDiscard) {
		t.Fatalf("全 pass 后 turn/phase=%d/%v", r.st.Turn, r.st.Phase)
	}
	if countRoomEvent(r, game.EventRon) != 0 {
		t.Fatal("pass 不应产生荣和")
	}
	if seen := drainClientTypes(t, clients[0]); seen["response_window_end"] == 0 {
		t.Fatalf("窗口结束应广播 response_window_end，实际=%v", seen)
	}
}

// TestResponseWindowTimeout 响应超时按 pass 结算（定时器路径）。
func TestResponseWindowTimeout(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 可荣和，但不响应
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	r, clients := craftRoom(t, st, roomConfig{responseTimeout: 20 * time.Millisecond})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	_ = readResponses(t, clients[1])

	select {
	case gen := <-r.timeoutCh:
		r.handleResponseTimeout(gen)
	case <-time.After(2 * time.Second):
		t.Fatal("等待响应超时信号失败")
	}
	if r.st.Turn != 1 || (r.st.Phase != game.PhaseAwaitDraw && r.st.Phase != game.PhaseAwaitDiscard) {
		t.Fatalf("超时后 turn/phase=%d/%v", r.st.Turn, r.st.Phase)
	}
	if countRoomEvent(r, game.EventRon) != 0 {
		t.Fatal("超时不应产生荣和")
	}
}

// TestResponseWindowMultiRon 多人抢和：双响都成立（Ruleset.DoubleRon）。
func TestResponseWindowMultiRon(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p",
		"123m456m789m11p34p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	st.Rules.DoubleRon = true
	r, clients := craftRoom(t, st, roomConfig{})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	_ = readResponses(t, clients[1])
	_ = readResponses(t, clients[2])
	if err := submitHuman(t, r, 1, game.Action{Kind: game.ActRon, Seat: 1, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("seat1 ron 失败: %v", err)
	}
	if err := submitHuman(t, r, 2, game.Action{Kind: game.ActRon, Seat: 2, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("seat2 ron 失败: %v", err)
	}
	if countRoomEvent(r, game.EventRon) != 2 {
		t.Fatalf("双响 ron 事件数=%d want 2", countRoomEvent(r, game.EventRon))
	}
	if r.st.Scores[1] != 28900 || r.st.Scores[2] != 28900 || r.st.Scores[0] != 17200 {
		t.Fatalf("双响点数=%v", r.st.Scores)
	}
}

// TestResponseWindowAISeat AI 座位自动决策并广播 agent_thought。
func TestResponseWindowAISeat(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p", // seat1 AI，可荣和
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	var agents [4]agent.Agent
	agents[1] = agent.FirstLegal{}
	r, clients := craftRoom(t, st, roomConfig{agents: agents, agentTimeout: 200 * time.Millisecond})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	select {
	case res := <-r.aiCh:
		if res.seat != 1 || !res.response {
			t.Fatalf("AI 结果=%+v，期望 seat1 的响应决策", res)
		}
		if res.action.Kind != game.ActRon {
			t.Fatalf("FirstLegal 应选 ron，实际 %v", res.action.Kind)
		}
		r.handleAI(res)
	case <-time.After(2 * time.Second):
		t.Fatal("等待 AI 响应决策超时")
	}
	if countRoomEvent(r, game.EventRon) != 1 {
		t.Fatalf("AI 荣和事件数=%d want 1", countRoomEvent(r, game.EventRon))
	}
	seen := drainClientTypes(t, clients[0])
	if seen["agent_thought"] == 0 {
		t.Fatalf("应广播 agent_thought，实际消息类型=%v", seen)
	}
	if seen["response_window_end"] == 0 {
		t.Fatalf("窗口结束应广播 response_window_end，实际消息类型=%v", seen)
	}
}

// TestResponseWindowNonPendingRejected 非待响应座位提交响应 → ILLEGAL_ACTION。
func TestResponseWindowNonPendingRejected(t *testing.T) {
	st := craftState(t,
		"1m1m2m2m3m3m4m4m6m6m7m8m9m5p",
		"123m456m789m11p34p",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
		"1m4m7m1s4s7s2p3p6p8p1z2z3z",
	)
	r, _ := craftRoom(t, st, roomConfig{})
	if err := submitHuman(t, r, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: craftTile(t, "5p")}); err != nil {
		t.Fatalf("打牌失败: %v", err)
	}
	// seat3 没有响应选项（已自动 pass），再次提交 pass 应被拒
	if err := submitHuman(t, r, 3, game.Action{Kind: game.ActPass, Seat: 3}); err == nil {
		t.Fatal("非待响应座位提交响应应报错")
	}
}

// TestResponseWindowOverWS 走真实 HTTP/WS：人类座位收到 {"type":"responses"}，
// 用同一 action 消息格式回 pass 后窗口推进。
func TestResponseWindowOverWS(t *testing.T) {
	const maxSeed = 40
	for seed := int64(1); seed <= maxSeed; seed++ {
		_, ts := newResponseTestServer(t, 1500*time.Millisecond)
		client := ts.Client()
		gameID := createGame(t, client, ts.URL, 0, map[string]any{
			"seed":   seed,
			"agents": []string{AgentHuman, AgentHuman, AgentFirstLegal, AgentFirstLegal},
		})
		conn0 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
		conn1 := dialWS(t, ts, gameID, 1, mustToken(t, 1))
		resumeFrom(t, conn0, 0)
		resumeFrom(t, conn1, 0)

		view := getState(t, client, ts.URL, gameID, 0)
		sendWS(t, conn0, map[string]any{
			"type":      "action",
			"seq":       stateSeq(t, view),
			"action_id": "ws-resp-1",
			"action":    map[string]any{"kind": "discard", "tile": drawnTile(t, view)},
		})

		// 等 seat1 的 responses（最多 300ms；没有就换 seed）
		_ = conn1.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		sawResponses := false
		for {
			var msg wsMessage
			if err := conn1.ReadJSON(&msg); err != nil {
				break
			}
			if msg.Type == "responses" {
				sawResponses = true
				sendWS(t, conn1, map[string]any{
					"type":      "action",
					"seq":       msg.Seq,
					"action_id": "ws-resp-pass",
					"action":    map[string]any{"kind": "pass"},
				})
				break
			}
			if msg.Type == "error" {
				break
			}
		}
		if !sawResponses {
			_ = conn0.Close()
			_ = conn1.Close()
			continue
		}
		// pass 后应继续收到事件（窗口推进）
		_ = conn1.SetReadDeadline(time.Now().Add(3 * time.Second))
		readEvents(t, conn1, 1)
		_ = conn0.Close()
		_ = conn1.Close()
		return
	}
	t.Fatalf("前 %d 个 seed 都没出现人类响应窗口", maxSeed)
}

// ── 端到端：1 真人 + 3 AI 打完整一局（副露 + 立直 + 和了）──────────

// riichiFirstAgent 是测试用 AI：能立直就立直（用于端到端覆盖立直），否则打第一张合法牌。
type riichiFirstAgent struct{}

// Name 实现 agent.Agent。
func (riichiFirstAgent) Name() string { return "test:riichi_first" }

// Decide 实现 agent.Agent。
func (riichiFirstAgent) Decide(_ context.Context, obs game.Observation) (game.Action, error) {
	for _, action := range obs.LegalActions {
		if action.Kind == game.ActRiichi {
			return action, nil
		}
	}
	if len(obs.LegalActions) == 0 {
		return game.Action{}, fmt.Errorf("riichiFirstAgent: 没有合法动作")
	}
	return obs.LegalActions[0], nil
}

// TestEndToEndHumanVsAIWithMeldWinAndRiichi 端到端：
//
//	seat0 人类：第一巡打 3m 给 seat1 吃；之后打 5p 点炮。
//	seat1 FirstLegal AI：吃 3m → 打 9s → 听 2p/5p → 荣和。
//	seat2 riichiFirst AI：第一巡立直（副露/立直/和了三种事件都出现）。
//	seat3 FirstLegal AI。
//
// 断言：事件里有 chi + riichi + ron；和了 payload 的 han/fu/points/score_deltas
// 与「rules 对已知手牌直算（含立直棒）」一致；最终点数一致。
func TestEndToEndHumanVsAIWithMeldWinAndRiichi(t *testing.T) {
	// seat1 吃 3m 后打 9s，留下 456m789m11p34p 听 2p/5p；
	// seat2 听 1p/2z（不会和 5p 撞车），第一巡立直；seat3 单骑 2z。
	st := craftState(t,
		"3m5p1p1p2p2p4p4p6p6p7p8p9p7z", // 占位 14 张，下面改成 13 张等摸牌
		"1m2m9s456m789m11p34p",         // seat1 AI
		"123m456m789m11p2z2z",          // seat2 AI（立直，听 1p/2z）
		"2s2s2s5s5s5s8s8s8s1z1z1z2z",   // seat3 AI（第一张 2s，单骑 2z）
	)
	st.Hands[0] = craftHand(t, "3m5p1p1p2p2p4p4p6p6p7p8p9p") // 13 张
	st.Phase = game.PhaseAwaitDraw
	st.Turn = 0
	st.Drawn = nil
	st.DrawCount = 0
	st.SeatDraws = [4]int{}
	indicators := st.Wall.DoraIndicators() // 本剧本无杠，表宝牌指示牌固定

	var agents [4]agent.Agent
	agents[1] = agent.FirstLegal{}
	agents[2] = riichiFirstAgent{}
	agents[3] = agent.FirstLegal{}

	r, _ := craftRoom(t, st, roomConfig{
		agents:          agents,
		agentTimeout:    200 * time.Millisecond,
		responseTimeout: 1 * time.Second,
	})
	r.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wanted := []string{"3m", "5p"}
	discardIdx := 0
	deadline := time.Now().Add(8 * time.Second)
	var lastSnap *roomSnapshot
	for time.Now().Before(deadline) {
		snap := r.Snapshot()
		if snap == nil {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		lastSnap = snap
		if snap.phase == game.PhaseRoundEnd || snap.phase == game.PhaseGameEnd {
			break
		}
		obs := snap.observations[0]
		if snap.phase == game.PhaseAwaitDiscard && snap.turn == 0 {
			var chosen *game.Action
			if discardIdx < len(wanted) {
				for i := range obs.LegalActions {
					legal := obs.LegalActions[i]
					if legal.Kind == game.ActDiscard && legal.Tile.String() == wanted[discardIdx] {
						pick := legal
						chosen = &pick
						break
					}
				}
			}
			if chosen == nil {
				for i := range obs.LegalActions {
					if obs.LegalActions[i].Kind == game.ActDiscard {
						pick := obs.LegalActions[i]
						chosen = &pick
						break
					}
				}
			}
			if chosen == nil {
				t.Fatalf("human 无牌可打: legal=%v", obs.LegalActions)
			}
			discardIdx++
			_ = r.SubmitAction(ctx, 0, *chosen, "", 0)
			time.Sleep(2 * time.Millisecond)
			continue
		}
		if snap.phase == game.PhaseAwaitResponse && len(obs.Responses) > 0 {
			_ = r.SubmitAction(ctx, 0, game.Action{Kind: game.ActPass, Seat: 0}, "", 0)
		}
		time.Sleep(2 * time.Millisecond)
	}

	if lastSnap == nil || lastSnap.phase != game.PhaseRoundEnd {
		phase := "nil"
		if lastSnap != nil {
			phase = lastSnap.phase.String()
		}
		t.Fatalf("对局未在本局结束，最后 phase=%s", phase)
	}

	r.Close()
	events, err := r.cfg.store.LoadEvents(context.Background(), r.id, 0)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	var chiEvent, riichiEvent, winEvent *game.Event
	for i := range events {
		ev := &events[i]
		switch ev.Kind {
		case game.EventChi:
			chiEvent = ev
		case game.EventRiichi:
			riichiEvent = ev
		case game.EventRon:
			winEvent = ev
		}
	}
	if chiEvent == nil {
		t.Fatalf("端到端对局应出现副露（chi），事件=%v", eventKinds(events))
	}
	if riichiEvent == nil {
		t.Fatalf("端到端对局应出现立直（riichi），事件=%v", eventKinds(events))
	}
	if winEvent == nil {
		t.Fatalf("端到端对局应出现和了（ron），事件=%v", eventKinds(events))
	}

	var payload map[string]any
	if err := json.Unmarshal(winEvent.Payload, &payload); err != nil {
		t.Fatalf("解析 ron payload: %v", err)
	}
	han := int(payload["han"].(float64))
	fu := int(payload["fu"].(float64))
	points := int(payload["points"].(float64))

	// rules 直算：seat1 的可见牌 = 吃 1m2m3m + 暗牌 456m789m11p34p + 和了牌 5p。
	// 立直棒由 game 引擎单独加给和了者（rules.Score 传 RiichiSticks=0 避免重复），
	// 所以 rules 直算的 Ron 不含立直棒，比较时再加 1000/根。
	winning := craftTile(t, "5p")
	closed := craftHand(t, "456m789m11p34p")
	closed = append(closed, winning)
	melds := []rules.Meld{{
		Kind:  rules.MeldChi,
		Tiles: []tile.Tile{craftTile(t, "1m"), craftTile(t, "2m"), craftTile(t, "3m")},
	}}
	shape, ok := rules.Decompose(closed, melds, winning)
	if !ok {
		t.Fatalf("rules 直算：副露和牌应成立（closed=%v melds=%v）", closed, melds)
	}
	scoreCtx := rules.ScoreContext{
		Menzen:         false,
		Tsumo:          false,
		SeatWind:       tile.South,
		RoundWind:      tile.East,
		RiichiSticks:   0,
		DoraIndicators: indicators,
	}
	_, yakuHan, rulesFu, err := rules.Evaluate(shape, scoreCtx)
	if err != nil {
		t.Fatalf("rules.Evaluate: %v", err)
	}
	scoreCtx.AkaDora = rules.CountAka(shape)
	dora := rules.CountDora(shape, scoreCtx)
	rulesPoints := rules.Score(yakuHan+dora, rulesFu, scoreCtx)

	stickBonus := 1000 // 场上 1 根立直棒（seat2 立直）
	wantPoints := rulesPoints.Ron + stickBonus
	if han != yakuHan+dora || fu != rulesFu {
		t.Fatalf("引擎 han/fu=%d/%d，rules 直算=%d/%d", han, fu, yakuHan+dora, rulesFu)
	}
	if points != wantPoints {
		t.Fatalf("引擎 points=%d，rules 直算荣和+立直棒=%d", points, wantPoints)
	}
	rawDeltas, ok := payload["score_deltas"].([]any)
	if !ok || len(rawDeltas) != 4 {
		t.Fatalf("payload.score_deltas 非法: %v", payload["score_deltas"])
	}
	if int(rawDeltas[1].(float64)) != wantPoints {
		t.Fatalf("和了者 delta=%v want +%d", rawDeltas[1], wantPoints)
	}
	loser := -1
	for seat := 0; seat < 4; seat++ {
		if int(rawDeltas[seat].(float64)) == -rulesPoints.Ron {
			loser = seat
		}
	}
	if loser < 0 {
		t.Fatalf("找不到支付 %d 点的放铳者: deltas=%v", rulesPoints.Ron, rawDeltas)
	}
	wantScores := [4]int{25000, 25000, 25000, 25000}
	wantScores[1] += wantPoints
	wantScores[loser] -= rulesPoints.Ron
	wantScores[2] -= 1000 // seat2 立直
	if lastSnap.observations[1].Scores != wantScores {
		t.Fatalf("最终点数=%v want %v", lastSnap.observations[1].Scores, wantScores)
	}
	t.Logf("端到端证据：事件=%v 副露=%s 立直=seat%d 和了=seat%d(ron) "+
		"han=%d fu=%d points=%d rules直算(不含棒)=%d 最终点数=%v",
		eventKinds(events), chiEvent.Payload, riichiEvent.Seat, winEvent.Seat,
		han, fu, points, rulesPoints.Ron, lastSnap.observations[1].Scores)
}

// eventKinds 提取事件类型序列（测试失败信息用）。
func eventKinds(events []game.Event) []game.EventKind {
	out := make([]game.EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}
