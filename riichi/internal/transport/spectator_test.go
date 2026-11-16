package transport

import "testing"

// TestWebSocketSpectatorReadOnlyAndBroadcast 观战连接：只读、收全量事件，多个观战端事件一致。
func TestWebSocketSpectatorReadOnlyAndBroadcast(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := startHumanGame(t, client, ts.URL, 42)

	sp1 := dialSpectatorWS(t, ts, gameID)
	sp2 := dialSpectatorWS(t, ts, gameID)

	events1, resumed1 := resumeFrom(t, sp1, 0)
	events2, resumed2 := resumeFrom(t, sp2, 0)
	if len(events1) != 2 || len(events2) != 2 {
		t.Fatalf("观战补齐事件数 seat1=%d seat2=%d，期望各 2 条", len(events1), len(events2))
	}
	if resumed1.ToSeq != resumed2.ToSeq {
		t.Fatalf("两个观战端 resumed.ToSeq 不一致: %d vs %d", resumed1.ToSeq, resumed2.ToSeq)
	}
	assertContiguous(t, events1, 1)

	// 真人 seat0 打出一张牌，事件应同步广播到两个观战端。
	seat0 := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	resumeFrom(t, seat0, 0)
	view := getState(t, client, ts.URL, gameID, 0)
	tile := drawnTile(t, view)
	sendWS(t, seat0, map[string]any{
		"type":      "action",
		"seq":       stateSeq(t, view),
		"action_id": "spectator-broadcast-1",
		"action":    map[string]any{"kind": "discard", "tile": tile},
	})

	got1 := readEvents(t, sp1, 2)
	got2 := readEvents(t, sp2, 2)
	for i := range got1 {
		if got1[i].Seq != got2[i].Seq || got1[i].Kind != got2[i].Kind {
			t.Fatalf("第 %d 条事件两个观战端不一致: %+v vs %+v", i, got1[i], got2[i])
		}
	}
	if got1[0].Kind != "discard" || got1[0].Seat != 0 {
		t.Fatalf("观战端应收到 discard 事件，实际 %+v", got1[0])
	}

	// 观战连接不能提交动作。
	sendWS(t, sp1, map[string]any{
		"type":   "action",
		"seq":    got1[1].Seq,
		"action": map[string]any{"kind": "discard", "tile": tile},
	})
	msg := readWS(t, sp1)
	if msg.Type != "error" || msg.Code != CodeIllegalAction {
		t.Fatalf("观战连接提交动作应回 ILLEGAL_ACTION，实际 %+v", msg)
	}
}
