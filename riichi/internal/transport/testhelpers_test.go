package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"summermahjong/riichi/internal/game"
)

const testSecret = "test-secret-0123456789abcdef"

// newTestServer 起一个进程内服务（httptest）。
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(Config{
		JWTSecret:    []byte(testSecret),
		AgentTimeout: 200 * time.Millisecond,
		Logger:       logger,
	})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Close()
	})
	return srv, ts
}

// mustToken 为座位签发测试令牌。
func mustToken(t *testing.T, seat game.Seat) string {
	t.Helper()
	token, err := SignToken([]byte(testSecret), seat, time.Hour)
	if err != nil {
		t.Fatalf("SignToken seat %d: %v", seat, err)
	}
	return token
}

// doJSON 发 JSON 请求，返回状态码与解析后的 JSON。
func doJSON(t *testing.T, client *http.Client, method, rawURL, token string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode body %q: %v", string(raw), err)
		}
	}
	return resp.StatusCode, decoded
}

// createGame 建局并返回 game_id。
func createGame(t *testing.T, client *http.Client, baseURL string, seat game.Seat, body map[string]any) string {
	t.Helper()
	status, decoded := doJSON(t, client, http.MethodPost, baseURL+"/api/v1/games", mustToken(t, seat), body)
	if status != http.StatusCreated {
		t.Fatalf("create game status=%d body=%v", status, decoded)
	}
	gameID, _ := decoded["game_id"].(string)
	if gameID == "" {
		t.Fatalf("create game 响应缺少 game_id: %v", decoded)
	}
	return gameID
}

// getState 用座位令牌取裁剪后的对局状态。
func getState(t *testing.T, client *http.Client, baseURL, gameID string, seat game.Seat) map[string]any {
	t.Helper()
	status, decoded := doJSON(t, client, http.MethodGet, baseURL+"/api/v1/games/"+gameID, mustToken(t, seat), nil)
	if status != http.StatusOK {
		t.Fatalf("get state status=%d body=%v", status, decoded)
	}
	return decoded
}

// wsURL 拼 WebSocket 地址。
func wsURL(ts *httptest.Server, gameID string, seat game.Seat, token string) string {
	base := "ws" + strings.TrimPrefix(ts.URL, "http")
	return fmt.Sprintf("%s/api/v1/ws/games/%s?seat=%d&token=%s",
		base, gameID, int(seat), url.QueryEscape(token))
}

// dialWS 建立 WS 连接。
func dialWS(t *testing.T, ts *httptest.Server, gameID string, seat game.Seat, token string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(ts, gameID, seat, token), nil)
	if err != nil {
		if resp != nil && resp.Body != nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("WS 连接失败 seat=%d status=%d body=%s err=%v", seat, resp.StatusCode, body, err)
		}
		t.Fatalf("WS 连接失败 seat=%d err=%v", seat, err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

// wsMessage 是测试侧的协议消息超集。
type wsMessage struct {
	Type    string          `json:"type"`
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	Seat    int             `json:"seat"`
	Payload map[string]any  `json:"payload"`
	FromSeq uint64          `json:"from_seq"`
	ToSeq   uint64          `json:"to_seq"`
	Code    string          `json:"code"`
	Message string          `json:"message"`
	Ts      json.RawMessage `json:"ts"`
}

// sendWS 发送一条客户端消息。
func sendWS(t *testing.T, conn *websocket.Conn, message any) {
	t.Helper()
	if err := conn.WriteJSON(message); err != nil {
		t.Fatalf("WS 写入失败: %v", err)
	}
}

// readWS 读取一条消息。
func readWS(t *testing.T, conn *websocket.Conn) wsMessage {
	t.Helper()
	var msg wsMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("WS 读取失败: %v", err)
	}
	return msg
}

// resumeFrom 发送 resume 并收集补齐事件，返回事件列表与 resumed 消息。
func resumeFrom(t *testing.T, conn *websocket.Conn, lastSeq uint64) ([]wsMessage, wsMessage) {
	t.Helper()
	sendWS(t, conn, map[string]any{"type": "resume", "last_seq": lastSeq})
	var events []wsMessage
	for {
		msg := readWS(t, conn)
		switch msg.Type {
		case "event":
			events = append(events, msg)
		case "resumed":
			return events, msg
		case "error":
			t.Fatalf("resume 返回错误: code=%s message=%s", msg.Code, msg.Message)
		default:
			t.Fatalf("resume 期间收到意外消息类型 %q", msg.Type)
		}
	}
}

// readEvents 连续读取 n 条 event 消息（忽略其它类型会直接失败）。
func readEvents(t *testing.T, conn *websocket.Conn, count int) []wsMessage {
	t.Helper()
	events := make([]wsMessage, 0, count)
	for len(events) < count {
		msg := readWS(t, conn)
		if msg.Type != "event" {
			t.Fatalf("期望 event，收到 type=%q code=%q message=%q", msg.Type, msg.Code, msg.Message)
		}
		events = append(events, msg)
	}
	return events
}

// assertContiguous 断言事件 seq 从 start 开始严格连续。
func assertContiguous(t *testing.T, events []wsMessage, start uint64) {
	t.Helper()
	for i, ev := range events {
		want := start + uint64(i)
		if ev.Seq != want {
			t.Fatalf("事件 seq 不连续：第 %d 条期望 %d，实际 %d", i, want, ev.Seq)
		}
	}
}

// waitFor 轮询直到条件成立或超时。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
