package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"summermahjong/riichi/internal/game"
)

// startHumanGame 建一局 4 个真人座位的对局（无 AI 自动推进，事件完全可预测）。
func startHumanGame(t *testing.T, client *http.Client, baseURL string, seed int64) string {
	t.Helper()
	return createGame(t, client, baseURL, 0, map[string]any{
		"seed":   seed,
		"agents": []string{"human", "human", "human", "human"},
	})
}

// stateSeq 取出状态里的 seq。
func stateSeq(t *testing.T, view map[string]any) uint64 {
	t.Helper()
	seq, ok := view["seq"].(float64)
	if !ok {
		t.Fatalf("状态里没有 seq: %v", view)
	}
	return uint64(seq)
}

// drawnTile 取出当前摸到的牌。
func drawnTile(t *testing.T, view map[string]any) string {
	t.Helper()
	tile, _ := view["drawn"].(string)
	if tile == "" {
		t.Fatalf("庄家摸牌后应有 drawn 字段: %v", view)
	}
	return tile
}

// TestWebSocketPingPong 断言心跳原样回显 ts。
func TestWebSocketPingPong(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	conn := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	sendWS(t, conn, map[string]any{"type": "ping", "ts": 123})
	msg := readWS(t, conn)
	if msg.Type != "pong" || string(msg.Ts) != "123" {
		t.Fatalf("ping/pong 异常: %+v", msg)
	}
}

// TestWebSocketBadMessages 断言非法 JSON / 未知类型只回 error，连接不断。
func TestWebSocketBadMessages(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)
	conn := dialWS(t, ts, gameID, 0, mustToken(t, 0))

	if err := conn.WriteMessage(websocket.TextMessage, []byte("not a json")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	msg := readWS(t, conn)
	if msg.Type != "error" || msg.Code != CodeValidation {
		t.Fatalf("非法 JSON 应回 VALIDATION error，实际 %+v", msg)
	}

	sendWS(t, conn, map[string]any{"type": "no-such-type"})
	msg = readWS(t, conn)
	if msg.Type != "error" || msg.Code != CodeValidation {
		t.Fatalf("未知消息类型应回 VALIDATION error，实际 %+v", msg)
	}

	// 连接仍然可用
	sendWS(t, conn, map[string]any{"type": "ping", "ts": 7})
	msg = readWS(t, conn)
	if msg.Type != "pong" || string(msg.Ts) != "7" {
		t.Fatalf("错误消息后连接应仍可用，实际 %+v", msg)
	}
}

// TestWebSocketBroadcastToAllSeats 两个客户端同时在线：一个打牌，两个都收到事件。
func TestWebSocketBroadcastToAllSeats(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	conn0 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	conn1 := dialWS(t, ts, gameID, 1, mustToken(t, 1))

	events0, resumed0 := resumeFrom(t, conn0, 0)
	events1, _ := resumeFrom(t, conn1, 0)
	if len(events0) != 2 || len(events1) != 2 {
		t.Fatalf("连接后应补齐 2 条事件，实际 seat0=%d seat1=%d", len(events0), len(events1))
	}
	assertContiguous(t, events0, 1)
	if resumed0.ToSeq != 2 {
		t.Fatalf("resumed.ToSeq=%d want 2", resumed0.ToSeq)
	}

	view := getState(t, client, ts.URL, gameID, 0)
	tile := drawnTile(t, view)
	sendWS(t, conn0, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view),
		"action_id": "broadcast-1",
		"action":    map[string]any{"kind": "discard", "tile": tile},
	})

	for name, conn := range map[string]*websocket.Conn{"seat0": conn0, "seat1": conn1} {
		got := readEvents(t, conn, 2)
		if got[0].Kind != "discard" || got[0].Seat != 0 {
			t.Fatalf("%s 第一条事件应为 seat0 discard，实际 %+v", name, got[0])
		}
		if got[1].Kind != "draw" || got[1].Seat != 1 {
			t.Fatalf("%s 第二条事件应为 seat1 draw，实际 %+v", name, got[1])
		}
		if got[0].Payload["tile"] != tile {
			t.Fatalf("%s discard payload=%v want tile=%s", name, got[0].Payload, tile)
		}
		if got[0].Payload["tsumogiri"] != true {
			t.Fatalf("%s 打出的应是刚摸到的牌（tsumogiri=true），payload=%v", name, got[0].Payload)
		}
	}
}

// TestWebSocketActionIdempotent 同一 action_id 重复提交只执行一次。
func TestWebSocketActionIdempotent(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)
	conn := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	_, _ = resumeFrom(t, conn, 0)

	view := getState(t, client, ts.URL, gameID, 0)
	tile := drawnTile(t, view)
	action := map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view),
		"action_id": "idem-1",
		"action":    map[string]any{"kind": "discard", "tile": tile},
	}

	sendWS(t, conn, action)
	readEvents(t, conn, 2) // discard + 下家摸牌

	// 重复同一 action_id：应被忽略（不产生新事件、也不报错）
	sendWS(t, conn, action)
	sendWS(t, conn, map[string]any{"type": "ping", "ts": 5})
	msg := readWS(t, conn)
	if msg.Type != "pong" {
		t.Fatalf("重复 action 后不应有新事件，实际收到 %+v", msg)
	}

	// 回放事件数不变（2 条初始 + 2 条新事件）
	status, replay := doJSON(t, client, http.MethodGet,
		ts.URL+"/api/v1/games/"+gameID+"/replay", mustToken(t, 0), nil)
	if status != http.StatusOK {
		t.Fatalf("replay status=%d", status)
	}
	events, _ := replay["events"].([]any)
	if len(events) != 4 {
		t.Fatalf("幂等失效：事件数=%d want 4", len(events))
	}
}

// TestWebSocketStaleSeq 客户端 seq 落后返回 STALE_SEQ，且不执行动作。
func TestWebSocketStaleSeq(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)
	conn := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	_, _ = resumeFrom(t, conn, 0)

	sendWS(t, conn, map[string]any{
		"type":      "action",
		"seq":       999,
		"action_id": "stale-1",
		"action":    map[string]any{"kind": "discard", "tile": "1m"},
	})
	msg := readWS(t, conn)
	if msg.Type != "error" || msg.Code != CodeStaleSeq {
		t.Fatalf("期望 STALE_SEQ error，实际 %+v", msg)
	}
}

// TestWebSocketIllegalAction 非法动作（M1 未实现 / 不是自己回合）返回 ILLEGAL_ACTION。
func TestWebSocketIllegalAction(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)
	conn0 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	conn1 := dialWS(t, ts, gameID, 1, mustToken(t, 1))
	_, _ = resumeFrom(t, conn0, 0)
	_, _ = resumeFrom(t, conn1, 0)

	// 先让 seat1 成为当前座位
	view := getState(t, client, ts.URL, gameID, 0)
	sendWS(t, conn0, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view),
		"action_id": "legal-1",
		"action":    map[string]any{"kind": "discard", "tile": drawnTile(t, view)},
	})
	readEvents(t, conn0, 2)
	readEvents(t, conn1, 2)

	// seat1 发一个 M1 未实现的动作（pon）
	view1 := getState(t, client, ts.URL, gameID, 1)
	sendWS(t, conn1, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view1),
		"action_id": "illegal-pon",
		"action":    map[string]any{"kind": "pon"},
	})
	msg := readWS(t, conn1)
	if msg.Type != "error" || msg.Code != CodeIllegalAction {
		t.Fatalf("期望 ILLEGAL_ACTION，实际 %+v", msg)
	}

	// seat0 不是当前座位，打牌也应被拒
	sendWS(t, conn0, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view1),
		"action_id": "illegal-seat0",
		"action":    map[string]any{"kind": "discard", "tile": "1m"},
	})
	msg = readWS(t, conn0)
	if msg.Type != "error" || msg.Code != CodeIllegalAction {
		t.Fatalf("非当前座位打牌应 ILLEGAL_ACTION，实际 %+v", msg)
	}
}

// TestWebSocketResumeNoNewEvents 在已同步状态下再次 resume 不重复发事件。
func TestWebSocketResumeNoNewEvents(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)
	conn := dialWS(t, ts, gameID, 0, mustToken(t, 0))

	events, _ := resumeFrom(t, conn, 0)
	assertContiguous(t, events, 1)

	again, resumed := resumeFrom(t, conn, uint64(len(events)))
	if len(again) != 0 {
		t.Fatalf("已同步后 resume 不应重复补事件，实际 %d 条", len(again))
	}
	if resumed.FromSeq != uint64(len(events)) || resumed.ToSeq != uint64(len(events)) {
		t.Fatalf("resumed=%+v，期望 from==to==%d", resumed, len(events))
	}
}

// TestWebSocketResumeAfterDisconnect 断线 → resume 补齐：不丢不重。
//
// 用 4 个真人座位确定性驱动：conn0 在 seq=2 断开，断线期间其它三个座位依次打牌
// （产生 8 条新事件），重连后 resume(2) 必须恰好补齐 seq 3..10。
func TestWebSocketResumeAfterDisconnect(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	// conn0 在 seq=2（round_start + 庄家摸牌）后断线
	conn0 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	first, _ := resumeFrom(t, conn0, 0)
	if len(first) != 2 {
		t.Fatalf("初始事件数=%d want 2", len(first))
	}
	lastSeq := uint64(2)
	_ = conn0.Close()

	// 断线期间：座位 0/1/2/3 各打一张牌，每次产生 discard + 下家 draw 两条事件
	for seat := game.Seat(0); seat < seatCount; seat++ {
		conn := dialWS(t, ts, gameID, seat, mustToken(t, seat))
		discardCurrent(t, client, ts.URL, gameID, seat, conn, fmt.Sprintf("resume-drive-%d", seat))
		_ = conn.Close()
	}

	// 重连 seat0：resume(2) 必须恰好补齐 3..10（不丢不重）
	conn2 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	second, resumed := resumeFrom(t, conn2, lastSeq)
	if len(second) != 8 {
		t.Fatalf("断线期间产生 8 条事件，补齐数=%d", len(second))
	}
	if resumed.FromSeq != lastSeq+1 || resumed.ToSeq != 10 {
		t.Fatalf("resumed=%+v，期望 from=3 to=10", resumed)
	}
	assertContiguous(t, second, lastSeq+1)

	// 与 replay 接口逐条对齐（seq/kind/seat/payload 一致）
	status, replay := doJSON(t, client, http.MethodGet,
		ts.URL+"/api/v1/games/"+gameID+"/replay", mustToken(t, 0), nil)
	if status != http.StatusOK {
		t.Fatalf("replay status=%d body=%v", status, replay)
	}
	events, _ := replay["events"].([]any)
	if len(events) != 10 {
		t.Fatalf("replay 事件数=%d want 10", len(events))
	}
	for _, ev := range second {
		raw, _ := events[ev.Seq-1].(map[string]any)
		if raw["kind"] != ev.Kind || raw["seat"] != float64(ev.Seat) {
			t.Fatalf("seq=%d 实时与回放不一致: %v vs %+v", ev.Seq, raw, ev)
		}
		if fmt.Sprint(raw["payload"]) != fmt.Sprint(ev.Payload) {
			t.Fatalf("seq=%d payload 不一致: %v vs %v", ev.Seq, raw["payload"], ev.Payload)
		}
	}
}

// discardCurrent 让指定座位打出一张牌，并等待状态推进两条事件（discard + 下家 draw）。
func discardCurrent(t *testing.T, client *http.Client, baseURL, gameID string, seat game.Seat, conn *websocket.Conn, actionID string) {
	t.Helper()
	view := getState(t, client, baseURL, gameID, seat)
	seq := stateSeq(t, view)
	sendWS(t, conn, map[string]any{
		"type":      "action",
		"seq":       seq,
		"action_id": actionID,
		"action":    map[string]any{"kind": "discard", "tile": drawnTile(t, view)},
	})
	advanced := waitFor(t, 2*time.Second, func() bool {
		return stateSeq(t, getState(t, client, baseURL, gameID, seat)) == seq+2
	})
	if !advanced {
		t.Fatalf("座位 %d 打牌后状态未推进", seat)
	}
}

// TestWebSocketSeatMismatch 令牌座位与 seat 参数不一致 → HTTP 403 SEAT_MISMATCH。
func TestWebSocketSeatMismatch(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, gameID, 0, mustToken(t, 1)), nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("座位不匹配的连接不应成功")
	}
	if resp == nil {
		t.Fatalf("缺少握手响应: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d want 403", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var body apiErrorBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("握手响应不是 JSON: %s", raw)
	}
	if body.Error.Code != CodeSeatMismatch {
		t.Fatalf("code=%s want %s", body.Error.Code, CodeSeatMismatch)
	}
}

// TestWebSocketUnauthorized 缺令牌 / 非法令牌 → 401。
func TestWebSocketUnauthorized(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	cases := map[string]string{
		"缺令牌":  "",
		"非法令牌": "not-a-jwt",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, gameID, 0, token), nil)
			if err == nil {
				_ = conn.Close()
				t.Fatal("缺/非法令牌不应连接成功")
			}
			if resp == nil {
				t.Fatalf("缺少握手响应: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status=%d want 401", resp.StatusCode)
			}
		})
	}
}

// TestWebSocketGameNotFound 对局不存在 → 404 GAME_NOT_FOUND。
func TestWebSocketGameNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, "no-such-game", 0, mustToken(t, 0)), nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("不存在的对局不应连接成功")
	}
	if resp == nil {
		t.Fatalf("缺少握手响应: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d want 404", resp.StatusCode)
	}
}

// TestWebSocketInvalidSeat seat 参数非法 → 400 VALIDATION。
func TestWebSocketInvalidSeat(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	base := "ws" + strings.TrimPrefix(ts.URL, "http")
	badURL := fmt.Sprintf("%s/api/v1/ws/games/%s?seat=9&token=%s",
		base, gameID, url.QueryEscape(mustToken(t, 0)))
	conn, resp, err := websocket.DefaultDialer.Dial(badURL, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("非法座位不应连接成功")
	}
	if resp == nil {
		t.Fatalf("缺少握手响应: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
}
