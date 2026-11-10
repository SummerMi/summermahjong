package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"summermahjong/riichi/internal/agent"
	"summermahjong/riichi/internal/game"
)

// ── 自动开下一局（整场连打）──────────────────────────────────────

// loadStoredEvents 读取房间事件存储（syncStore 并发安全）。
func loadStoredEvents(t *testing.T, r *Room) []game.Event {
	t.Helper()
	events, err := r.cfg.store.LoadEvents(context.Background(), r.id, 0)
	if err != nil {
		t.Fatalf("LoadEvents: %v", err)
	}
	return events
}

// countEventKind 统计事件类型数量。
func countEventKind(events []game.Event, kind game.EventKind) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

// decodePayload 把事件 payload 解到 map。
func decodePayload(t *testing.T, ev game.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("解析 %v payload: %v", ev.Kind, err)
	}
	return payload
}

// payloadIntField 读 payload 的整数字段。
func payloadIntField(t *testing.T, payload map[string]any, key string) int {
	t.Helper()
	value, ok := payload[key]
	if !ok {
		t.Fatalf("payload 缺少 %q: %v", key, payload)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("payload[%q]=%v 不是数字", key, value)
	}
	return int(number)
}

// newAllFirstLegalRoom 构造一局「四家 FirstLegal」的自动推进房间。
func newAllFirstLegalRoom(t *testing.T, rules game.Ruleset, autoAdvance bool) *Room {
	t.Helper()
	st, err := game.NewWithOptions(1, rules, game.Options{
		ManualResponses: false, // 自动 pass，全 AI 局快速推进
		ScoreProvider:   game.RulesScorer{},
	})
	if err != nil {
		t.Fatalf("NewWithOptions: %v", err)
	}
	var agents [4]agent.Agent
	for seat := 0; seat < 4; seat++ {
		agents[seat] = agent.FirstLegal{}
	}
	r, _ := craftRoom(t, st, roomConfig{
		agents:            agents,
		agentTimeout:      50 * time.Millisecond,
		responseTimeout:   50 * time.Millisecond,
		autoAdvanceRounds: autoAdvance,
	})
	t.Cleanup(r.Close)
	return r
}

// TestAutoAdvanceRoundsContinuous 连续跑 ≥3 局：断言 round_end→round_start 交替、
// 局数/亲家/本场推进符合连庄规则、事件正常落库。
func TestAutoAdvanceRoundsContinuous(t *testing.T) {
	r := newAllFirstLegalRoom(t, game.DefaultRuleset(), true)
	r.Start()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countEventKind(loadStoredEvents(t, r), game.EventRoundStart) >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	events := loadStoredEvents(t, r)
	if got := countEventKind(events, game.EventRoundStart); got < 3 {
		t.Fatalf("应至少自动开 3 局，实际 round_start=%d", got)
	}
	if got := countEventKind(events, game.EventRoundEnd); got < 2 {
		t.Fatalf("应有至少 2 次 round_end，实际 %d", got)
	}

	// 校验相邻两局的轮转/连庄逻辑
	type roundStart struct {
		wind   int
		kyoku  int
		honba  int
		dealer int
	}
	var starts []roundStart
	var lastEnd *map[string]any
	for i := range events {
		ev := &events[i]
		switch ev.Kind {
		case game.EventRoundEnd:
			payload := decodePayload(t, *ev)
			lastEnd = &payload
		case game.EventRoundStart:
			payload := decodePayload(t, *ev)
			starts = append(starts, roundStart{
				wind:   payloadIntField(t, payload, "round_wind"),
				kyoku:  payloadIntField(t, payload, "kyoku"),
				honba:  payloadIntField(t, payload, "honba"),
				dealer: payloadIntField(t, payload, "dealer"),
			})
			// 第 2 局起：必须紧接着上一局的 round_end（中间不能有 game_end）
			if len(starts) > 1 {
				if lastEnd == nil {
					t.Fatalf("第 %d 个 round_start 前没有 round_end", len(starts))
				}
			}
			lastEnd = nil
		}
	}

	for i := 1; i < len(starts); i++ {
		prev, cur := starts[i-1], starts[i]
		// 用相邻 round_end 判断连庄；这里按「本场增加 == 连庄」与「本场归零 == 轮转」校验
		if cur.honba == prev.honba+1 {
			// 连庄：亲家不变、局数不变
			if cur.dealer != prev.dealer || cur.kyoku != prev.kyoku || cur.wind != prev.wind {
				t.Fatalf("连庄局信息不一致: %+v → %+v", prev, cur)
			}
		} else if cur.honba == 0 {
			// 轮转：亲家 +1（取模 4），局数 +1（东 4 → 南 1）
			if cur.dealer != (prev.dealer+1)%4 {
				t.Fatalf("轮转亲家错误: %+v → %+v", prev, cur)
			}
			wantKyoku, wantWind := prev.kyoku+1, prev.wind
			if wantKyoku > 4 {
				wantKyoku, wantWind = 1, prev.wind+1
			}
			if cur.kyoku != wantKyoku || cur.wind != wantWind {
				t.Fatalf("轮转局数错误: %+v → %+v（期望 %v 风 %d 局）", prev, cur, wantWind, wantKyoku)
			}
		} else {
			t.Fatalf("本场数不连续: %+v → %+v", prev, cur)
		}
	}
	t.Logf("连打证据：round_start=%d round_end=%d 总事件=%d",
		len(starts), countEventKind(events, game.EventRoundEnd), len(events))
}

// TestAutoAdvanceReachesGameEnd 单局场（Ruleset.RoundLimit=1）自动推进到 game_end：
// 断言 game_end 含最终点数/顺位，且是最后一条事件、房间停住。
func TestAutoAdvanceReachesGameEnd(t *testing.T) {
	rules := game.DefaultRuleset()
	rules.RoundLimit = 1 // 只打一局：第一局结束即终局，便于确定性断言
	r := newAllFirstLegalRoom(t, rules, true)
	r.Start()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countEventKind(loadStoredEvents(t, r), game.EventGameEnd) > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	events := loadStoredEvents(t, r)
	if countEventKind(events, game.EventGameEnd) != 1 {
		t.Fatalf("应有 1 条 game_end，实际 %d（事件=%v）",
			countEventKind(events, game.EventGameEnd), eventKinds(events))
	}
	if events[len(events)-1].Kind != game.EventGameEnd {
		t.Fatalf("game_end 应为最后一条事件，实际最后=%v", events[len(events)-1].Kind)
	}
	payload := decodePayload(t, events[len(events)-1])
	scores, ok := payload["final_scores"].([]any)
	if !ok || len(scores) != 4 {
		t.Fatalf("game_end.final_scores 非法: %v", payload["final_scores"])
	}
	ranks, ok := payload["ranks"].([]any)
	if !ok || len(ranks) != 4 {
		t.Fatalf("game_end.ranks 非法: %v", payload["ranks"])
	}
	for _, rank := range ranks {
		value := int(rank.(float64))
		if value < 1 || value > 4 {
			t.Fatalf("顺位越界: %v", ranks)
		}
	}
	// 房间应停在 game_end（drive 不再推进）
	time.Sleep(20 * time.Millisecond)
	if snap := r.Snapshot(); snap == nil || snap.phase != game.PhaseGameEnd {
		t.Fatalf("终局后 room phase=%v want game_end", snap.phase)
	}
	t.Logf("终局证据：事件=%d 最终点数=%v 顺位=%v", len(events), scores, ranks)
}

// wsGameResult 是一局「WS 观战 + 自动连打整场」的结果。
type wsGameResult struct {
	seed       int64
	rounds     int // round_start 条数
	events     int // 收到的 event 总数
	scores     []any
	ranks      []any
	crossRound bool // 是否观察到 round_end → round_start
}

// runAutoAdvanceGameWS 建一局 3 random + 1 first_legal，连到 seat0 读到 game_end。
func runAutoAdvanceGameWS(t *testing.T, ts *httptest.Server, seed int64) wsGameResult {
	t.Helper()
	client := ts.Client()
	gameID := createGame(t, client, ts.URL, 0, map[string]any{
		"seed":   seed,
		"agents": []string{"first_legal", "first_legal", "first_legal", "random"},
	})
	conn := dialWS(t, ts, gameID, 0, mustToken(t, 0))
	initialEvents, _ := resumeFrom(t, conn, 0)

	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	res := wsGameResult{seed: seed}
	lastWasRoundEnd := false
	var gameEndPayload map[string]any
	for _, msg := range initialEvents {
		res.events++
		if msg.Kind == string(game.EventRoundStart) {
			res.rounds++
		}
	}
	for gameEndPayload == nil {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("等待 game_end 超时: %v（已收事件 %d，round_start=%d）", err, res.events, res.rounds)
		}
		if msg.Type != "event" {
			continue // agent_thought / responses 等非事件消息
		}
		res.events++
		switch msg.Kind {
		case string(game.EventRoundStart):
			res.rounds++
			if lastWasRoundEnd {
				res.crossRound = true
			}
			lastWasRoundEnd = false
		case string(game.EventRoundEnd):
			lastWasRoundEnd = true
		case string(game.EventGameEnd):
			gameEndPayload = msg.Payload
		}
	}
	scores, ok := gameEndPayload["final_scores"].([]any)
	if !ok || len(scores) != 4 {
		t.Fatalf("seed=%d game_end.final_scores 非法: %v", seed, gameEndPayload["final_scores"])
	}
	ranks, ok := gameEndPayload["ranks"].([]any)
	if !ok || len(ranks) != 4 {
		t.Fatalf("seed=%d game_end.ranks 非法: %v", seed, gameEndPayload["ranks"])
	}
	res.scores, res.ranks = scores, ranks
	return res
}

// TestAutoAdvanceRoundsOverWS 真实 HTTP/WS 端到端：4 个 AI（3 first_legal + 1 random）
// 自动连打整场；客户端能按顺序收到 round_end → round_start 跨局序列，最终收到 game_end。
func TestAutoAdvanceRoundsOverWS(t *testing.T) {
	_, ts := newAutoAdvanceTestServer(t)
	// 固定 seed 的 random 策略可能全场四家同分；挑一个出现点数变化的 seed 作为证据。
	var result wsGameResult
	for seed := int64(1); seed <= 12; seed++ {
		result = runAutoAdvanceGameWS(t, ts, seed)
		if !result.crossRound {
			t.Fatalf("seed=%d 没有观察到 round_end → round_start 的跨局序列", seed)
		}
		changed := false
		for _, score := range result.scores {
			if int(score.(float64)) != 25000 {
				changed = true
			}
		}
		if changed {
			break
		}
	}
	if !result.crossRound {
		t.Fatal("没有观察到跨局序列")
	}
	t.Logf("端到端整场证据（seed=%d）：round_start=%d 总事件=%d 终局点数=%v 顺位=%v",
		result.seed, result.rounds, result.events, result.scores, result.ranks)
}
