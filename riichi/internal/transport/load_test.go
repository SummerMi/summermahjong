package transport

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSpectatorBroadcastLatency100 100 条观战 WS 连接同时在线时，
// 测「seat0 提交 discard → 每个观战端收到 discard 事件」的端到端延迟分布。
//
// 口径：单房间 100 并发观战连接（本机 loopback），只断言一个宽松上限，
// 具体 p50/p95/p99 通过 -v 日志记录到文档。
func TestSpectatorBroadcastLatency100(t *testing.T) {
	if testing.Short() {
		t.Skip("压测用例在 -short 下跳过")
	}
	const connections = 100

	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	conns := make([]*websocket.Conn, 0, connections)
	for i := 0; i < connections; i++ {
		conn := dialSpectatorWS(t, ts, gameID)
		resumeFrom(t, conn, 0)
		conns = append(conns, conn)
	}

	seat0 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	resumeFrom(t, seat0, 0)
	view := getState(t, client, ts.URL, gameID, 0)
	tile := drawnTile(t, view)

	started := time.Now()
	sendWS(t, seat0, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view),
		"action_id": "load-100",
		"action":    map[string]any{"kind": "discard", "tile": tile},
	})

	durations := make([]time.Duration, connections)
	errs := make(chan error, connections)
	var wg sync.WaitGroup
	for i, conn := range conns {
		wg.Add(1)
		go func(i int, conn *websocket.Conn) {
			defer wg.Done()
			var msg wsMessage
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := conn.ReadJSON(&msg); err != nil {
				errs <- err
				return
			}
			durations[i] = time.Since(started)
		}(i, conn)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("观战端读取事件失败: %v", err)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p := func(q float64) time.Duration {
		idx := int(float64(len(durations)-1) * q)
		return durations[idx]
	}
	t.Logf("100 并发观战 broadcast 延迟：p50=%s p95=%s p99=%s max=%s",
		p(0.50).Round(time.Microsecond), p(0.95).Round(time.Microsecond),
		p(0.99).Round(time.Microsecond), durations[len(durations)-1].Round(time.Microsecond))
	if p99 := p(0.99); p99 > 50*time.Millisecond {
		t.Fatalf("p99=%s 超过 50ms 验收线", p99)
	}
}
