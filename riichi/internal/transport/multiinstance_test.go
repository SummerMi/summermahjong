package transport

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"summermahjong/riichi/internal/game"
	"summermahjong/riichi/internal/store"
	redisstore "summermahjong/riichi/internal/store/redis"
)

// ── 跨实例总线 ──────────────────────────────────────────────────

func TestMemoryBusPubSub(t *testing.T) {
	bus := NewMemoryBus()
	ch, cancel, err := bus.Subscribe(context.Background(), "chan")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := bus.Publish(context.Background(), "chan", []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got := <-ch:
		if string(got) != "hello" {
			t.Fatalf("收到 %q want hello", got)
		}
	case <-time.After(time.Second):
		t.Fatal("MemoryBus 未投递")
	}
}

func TestRedisBusPubSub(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()

	bus := NewRedisBus(client)
	ch, cancel, err := bus.Subscribe(context.Background(), "chan")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	if err := bus.Publish(context.Background(), "chan", []byte("hello")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got := <-ch:
		if string(got) != "hello" {
			t.Fatalf("收到 %q want hello", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RedisBus 未投递")
	}
}

// ── 跨实例观战镜像 ──────────────────────────────────────────────

// readEventFrames 从连接发送队列里读 n 条 event 帧（跳过 resumed 等非事件消息）。
func readEventFrames(t *testing.T, c *client, n int) []wsMessage {
	t.Helper()
	events := make([]wsMessage, 0, n)
	deadline := time.After(3 * time.Second)
	for len(events) < n {
		select {
		case data := <-c.send:
			var msg wsMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("解析帧: %v", err)
			}
			if msg.Type == "event" {
				events = append(events, msg)
			}
		case <-deadline:
			t.Fatalf("等待 %d 条事件超时（已收到 %d 条）", n, len(events))
		}
	}
	return events
}

// TestRemoteSpectatorMirror 两个实例共享 store + bus：本实例观战端与远端镜像观战端事件逐条一致。
func TestRemoteSpectatorMirror(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := store.NewMemory()
	bus := NewMemoryBus()
	mgrA := NewManager(ManagerConfig{Store: st, Bus: bus, AgentTimeout: time.Second})
	defer mgrA.Close()
	mgrB := NewManager(ManagerConfig{Store: st, Bus: bus, AgentTimeout: time.Second})
	defer mgrB.Close()

	room, err := mgrA.Create(ctx, CreateSpec{
		Seed:   42,
		Agents: []string{"human", "human", "human", "human"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessA, cA, err := mgrA.AttachSpectator(ctx, room.ID())
	if err != nil {
		t.Fatalf("AttachSpectator(A): %v", err)
	}
	if err := sessA.Resume(ctx, cA, 0); err != nil {
		t.Fatalf("Resume(A): %v", err)
	}
	sessB, cB, err := mgrB.AttachSpectator(ctx, room.ID())
	if err != nil {
		t.Fatalf("AttachSpectator(B): %v", err)
	}
	if err := sessB.Resume(ctx, cB, 0); err != nil {
		t.Fatalf("Resume(B): %v", err)
	}

	// 开局后应各有 2 条历史事件（round_start + draw）。
	local := readEventFrames(t, cA, 2)
	remote := readEventFrames(t, cB, 2)
	for i := range local {
		if local[i].Seq != remote[i].Seq || local[i].Kind != remote[i].Kind {
			t.Fatalf("历史事件不一致（第 %d 条）：local=%+v remote=%+v", i, local[i], remote[i])
		}
	}

	// 本实例执行一次打牌，两端应收到同一批实时事件。
	snap := room.Snapshot()
	if snap == nil || snap.observations[0].Drawn == nil {
		t.Fatal("快照里没有庄家摸到的牌")
	}
	drawn := *snap.observations[0].Drawn
	if err := room.SubmitAction(ctx, 0, game.Action{Kind: game.ActDiscard, Seat: 0, Tile: drawn}, "remote-1", snap.seq); err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	local = readEventFrames(t, cA, 2)
	remote = readEventFrames(t, cB, 2)
	for i := range local {
		if local[i].Seq != remote[i].Seq || local[i].Kind != remote[i].Kind {
			t.Fatalf("实时事件不一致（第 %d 条）：local=%+v remote=%+v", i, local[i], remote[i])
		}
	}
	if local[0].Kind != "discard" || local[0].Seat != 0 {
		t.Fatalf("首条实时事件应为 seat0 discard，实际 %+v", local[0])
	}
}

// TestTwoServerSpectatorFanout 两个服务实例共享 Redis store + pub/sub：
// 观战端分别连 A/B，应收到逐条一致的事件（真正的两进程路径，用 miniredis 模拟 Redis）。
func TestTwoServerSpectatorFanout(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()
	shared := redisstore.New(client)
	bus := NewRedisBus(client)

	_, tsA := newTestServerWithConfig(t, Config{
		JWTSecret:         []byte(testSecret),
		Store:             shared,
		Bus:               bus,
		AutoPassResponses: true,
		AutoAdvanceRounds: boolPtr(false),
	})
	_, tsB := newTestServerWithConfig(t, Config{
		JWTSecret:         []byte(testSecret),
		Store:             shared,
		Bus:               bus,
		AutoPassResponses: true,
		AutoAdvanceRounds: boolPtr(false),
	})

	httpClient := tsA.Client()
	gameID := startHumanGame(t, httpClient, tsA.URL, 42)

	spA := dialSpectatorWS(t, tsA, gameID)
	spB := dialSpectatorWS(t, tsB, gameID)
	evA, _ := resumeFrom(t, spA, 0)
	evB, _ := resumeFrom(t, spB, 0)
	if len(evA) != 2 || len(evB) != 2 {
		t.Fatalf("观战补齐事件数 A=%d B=%d，期望各 2 条", len(evA), len(evB))
	}
	for i := range evA {
		if evA[i].Seq != evB[i].Seq || evA[i].Kind != evB[i].Kind {
			t.Fatalf("历史事件跨实例不一致（第 %d 条）：A=%+v B=%+v", i, evA[i], evB[i])
		}
	}

	// seat0 连 A 打一张，A/B 的观战端应收到同一批事件。
	conn0 := dialWS(t, tsA, gameID, 0, mustToken(t, 0))
	resumeFrom(t, conn0, 0)
	view := getState(t, httpClient, tsA.URL, gameID, 0)
	tile := drawnTile(t, view)
	sendWS(t, conn0, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view),
		"action_id": "two-server-1",
		"action":    map[string]any{"kind": "discard", "tile": tile},
	})

	gotA := readEvents(t, spA, 2)
	gotB := readEvents(t, spB, 2)
	for i := range gotA {
		if gotA[i].Seq != gotB[i].Seq || gotA[i].Kind != gotB[i].Kind {
			t.Fatalf("实时事件跨实例不一致（第 %d 条）：A=%+v B=%+v", i, gotA[i], gotB[i])
		}
	}
	if gotA[0].Kind != "discard" || gotA[0].Seat != 0 {
		t.Fatalf("首条实时事件应为 seat0 discard，实际 %+v", gotA[0])
	}
}
