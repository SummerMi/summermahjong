package transport

import (
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSpectatorBroadcastLatency25Rooms 验收口径：100 条 WS 连接分布在 25 个房间，
// 测「每个房间 seat0 提交 discard → 该房间 4 个观战端收到事件」的端到端延迟。
func TestSpectatorBroadcastLatency25Rooms(t *testing.T) {
	if testing.Short() {
		t.Skip("压测用例在 -short 下跳过")
	}
	const (
		rooms       = 25
		perRoom     = 4
		connections = rooms * perRoom
	)

	_, ts := newTestServer(t)
	client := ts.Client()
	type roomCase struct {
		gameID string
		conns  []*websocket.Conn
		seat0  *websocket.Conn
		view   map[string]any
	}
	cases := make([]*roomCase, 0, rooms)
	for i := 0; i < rooms; i++ {
		gameID := startHumanGame(t, client, ts.URL, int64(100+i))
		rc := &roomCase{gameID: gameID}
		for j := 0; j < perRoom; j++ {
			conn := dialSpectatorWS(t, ts, gameID)
			resumeFrom(t, conn, 0)
			rc.conns = append(rc.conns, conn)
		}
		rc.seat0 = dialWS(t, ts, gameID, 0, mustToken(t, 0))
		resumeFrom(t, rc.seat0, 0)
		rc.view = getState(t, client, ts.URL, gameID, 0)
		cases = append(cases, rc)
	}

	durations := make([]time.Duration, 0, connections)
	started := time.Now()
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make(chan error, connections)
	for _, rc := range cases {
		tile := drawnTile(t, rc.view)
		sendWS(t, rc.seat0, map[string]any{
			"type":      "action",
			"seq":       stateSeq(t, rc.view),
			"action_id": "load-25x4-" + rc.gameID,
			"action":    map[string]any{"kind": "discard", "tile": tile},
		})
		for _, conn := range rc.conns {
			wg.Add(1)
			go func(conn *websocket.Conn) {
				defer wg.Done()
				var msg wsMessage
				_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				if err := conn.ReadJSON(&msg); err != nil {
					errs <- err
					return
				}
				mu.Lock()
				durations = append(durations, time.Since(started))
				mu.Unlock()
			}(conn)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("观战端读取事件失败: %v", err)
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p := func(q float64) time.Duration {
		return durations[int(float64(len(durations)-1)*q)]
	}
	t.Logf("25 房间 / 100 连接 broadcast 延迟：p50=%s p95=%s p99=%s max=%s",
		p(0.50).Round(time.Microsecond), p(0.95).Round(time.Microsecond),
		p(0.99).Round(time.Microsecond), durations[len(durations)-1].Round(time.Microsecond))
	if p99 := p(0.99); p99 > 50*time.Millisecond {
		t.Fatalf("p99=%s 超过 50ms 验收线", p99)
	}
}
