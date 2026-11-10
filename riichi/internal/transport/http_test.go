package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHealthz 健康检查。
func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)
	status, body := doJSON(t, ts.Client(), http.MethodGet, ts.URL+"/healthz", "", nil)
	if status != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("healthz status=%d body=%v", status, body)
	}
}

// TestCreateGameRequiresToken 断言建局必须带合法 JWT。
func TestCreateGameRequiresToken(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()

	status, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/games", "", map[string]any{})
	if status != http.StatusUnauthorized {
		t.Fatalf("无令牌建局 status=%d body=%v", status, body)
	}
	assertErrorCode(t, body, CodeUnauthorized)

	status, body = doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/games", "not-a-jwt", map[string]any{})
	if status != http.StatusUnauthorized {
		t.Fatalf("非法令牌建局 status=%d body=%v", status, body)
	}
	assertErrorCode(t, body, CodeUnauthorized)

	expired, err := signToken([]byte(testSecret), 0, time.Now(), -time.Minute)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	status, body = doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/games", expired, map[string]any{})
	if status != http.StatusUnauthorized {
		t.Fatalf("过期令牌建局 status=%d body=%v", status, body)
	}
}

// TestCreateGameDefaults 断言省略字段时的默认值：1 人类 + 3 AI、天鳳般规则。
func TestCreateGameDefaults(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()

	status, created := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/games",
		mustToken(t, 1), map[string]any{"seed": 7})
	if status != http.StatusCreated {
		t.Fatalf("建局 status=%d body=%v", status, created)
	}
	agents, _ := created["agents"].([]any)
	want := []string{AgentHuman, AgentRandom, AgentRandom, AgentRandom}
	if len(agents) != len(want) {
		t.Fatalf("默认 agents=%v", agents)
	}
	for i := range want {
		if agents[i] != want[i] {
			t.Fatalf("默认 agents=%v want %v", agents, want)
		}
	}
	ruleset, _ := created["ruleset"].(map[string]any)
	if ruleset["red_fives"] != float64(3) || ruleset["starting_points"] != float64(25000) {
		t.Fatalf("默认 ruleset=%v", ruleset)
	}
	if created["game_id"] == "" {
		t.Fatalf("缺少 game_id: %v", created)
	}
}

// TestCreateGameValidation 断言建局参数校验（400 VALIDATION）。
func TestCreateGameValidation(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	token := mustToken(t, 0)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"agents 数量不对", map[string]any{"agents": []string{"random"}}},
		{"未知 agent", map[string]any{"agents": []string{"human", "gpt", "random", "random"}}},
		{"赤宝牌数量越界", map[string]any{"ruleset": map[string]any{"red_fives": 5}}},
		{"赤宝牌为负", map[string]any{"ruleset": map[string]any{"red_fives": -1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := doJSON(t, client, http.MethodPost, ts.URL+"/api/v1/games", token, tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d body=%v", status, body)
			}
			assertErrorCode(t, body, CodeValidation)
		})
	}

	// 非法 JSON 请求体
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/games", strings.NewReader(`{"seed":`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON status=%d body=%s", resp.StatusCode, raw)
	}
}

// TestObserveTrimming 断言 GET 状态按座位裁剪：只返回自己手牌。
func TestObserveTrimming(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := createGame(t, client, ts.URL, 0,
		map[string]any{"seed": 42, "agents": []string{"human", "human", "human", "human"}})

	view0 := getState(t, client, ts.URL, gameID, 0)
	view1 := getState(t, client, ts.URL, gameID, 1)

	if view0["seat"] != float64(0) || view1["seat"] != float64(1) {
		t.Fatalf("座位裁剪错误: seat0=%v seat1=%v", view0["seat"], view1["seat"])
	}
	hand0, _ := view0["hand"].([]any)
	hand1, _ := view1["hand"].([]any)
	if len(hand0) != 14 {
		t.Fatalf("庄家摸牌后应有 14 张，实际 %d 张: %v", len(hand0), hand0)
	}
	if len(hand1) != 13 {
		t.Fatalf("闲家应有 13 张，实际 %d 张", len(hand1))
	}
	if fmt.Sprint(hand0) == fmt.Sprint(hand1) {
		t.Fatal("不同座位不应拿到同一手牌（裁剪失效）")
	}

	legal0, _ := view0["legal_actions"].([]any)
	legal1, _ := view1["legal_actions"].([]any)
	if len(legal0) != 14 {
		t.Fatalf("当前座位应有 14 个打牌动作，实际 %d", len(legal0))
	}
	if len(legal1) != 0 {
		t.Fatalf("非当前座位不应有合法动作，实际 %d", len(legal1))
	}
	if view0["phase"] != "await_discard" {
		t.Fatalf("phase=%v want await_discard", view0["phase"])
	}
	if view0["wall_remaining"] != float64(69) {
		t.Fatalf("摸一张后牌墙应剩 69，实际 %v", view0["wall_remaining"])
	}
}

// TestGetGameNotFound 断言查询不存在的对局返回 404 GAME_NOT_FOUND。
func TestGetGameNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	status, body := doJSON(t, ts.Client(), http.MethodGet,
		ts.URL+"/api/v1/games/does-not-exist", mustToken(t, 0), nil)
	if status != http.StatusNotFound {
		t.Fatalf("status=%d body=%v", status, body)
	}
	assertErrorCode(t, body, CodeGameNotFound)
}

// TestReplayEvents 断言回放接口返回全量、带 payload 的事件。
func TestReplayEvents(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := createGame(t, client, ts.URL, 0,
		map[string]any{"seed": 42, "agents": []string{"human", "human", "human", "human"}})

	status, body := doJSON(t, client, http.MethodGet,
		ts.URL+"/api/v1/games/"+gameID+"/replay", mustToken(t, 3), nil)
	if status != http.StatusOK {
		t.Fatalf("replay status=%d body=%v", status, body)
	}
	events, _ := body["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("初始应有 round_start + draw 两条事件，实际 %d 条: %v", len(events), events)
	}
	first, _ := events[0].(map[string]any)
	second, _ := events[1].(map[string]any)
	if first["kind"] != "round_start" || first["seq"] != float64(1) {
		t.Fatalf("首条事件=%v", first)
	}
	if second["kind"] != "draw" || second["seat"] != float64(0) {
		t.Fatalf("第二条事件=%v", second)
	}
	payload, _ := second["payload"].(map[string]any)
	if payload["wall_remaining"] != float64(69) {
		t.Fatalf("draw payload=%v", payload)
	}
	if _, ok := payload["tile"].(string); !ok {
		t.Fatalf("draw payload 缺少 tile: %v", payload)
	}
}

// TestMetricsEndpoints 断言进程指标与单局指标。
func TestMetricsEndpoints(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := createGame(t, client, ts.URL, 0, map[string]any{"seed": 1})

	status, body := doJSON(t, client, http.MethodGet, ts.URL+"/metrics", "", nil)
	if status != http.StatusOK {
		t.Fatalf("metrics status=%d body=%v", status, body)
	}
	if body["games_created"] != float64(1) {
		t.Fatalf("games_created=%v", body["games_created"])
	}
	if _, ok := body["decision_latency"]; !ok {
		t.Fatalf("metrics 缺少 decision_latency: %v", body)
	}

	status, body = doJSON(t, client, http.MethodGet,
		ts.URL+"/api/v1/games/"+gameID+"/metrics", mustToken(t, 0), nil)
	if status != http.StatusOK {
		t.Fatalf("game metrics status=%d body=%v", status, body)
	}
	if body["game_id"] != gameID {
		t.Fatalf("game_id=%v want %s", body["game_id"], gameID)
	}
	if _, ok := body["events_persisted"]; !ok {
		t.Fatalf("单局指标缺少 events_persisted: %v", body)
	}

	status, _ = doJSON(t, client, http.MethodGet, ts.URL+"/api/v1/games/"+gameID+"/metrics", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("单局指标无令牌 status=%d", status)
	}
}

// TestAIProgress 断言 AI 座位会自动推进对局，且指标记录了决策。
func TestAIProgress(t *testing.T) {
	_, ts := newTestServer(t)
	client := ts.Client()
	gameID := createGame(t, client, ts.URL, 0, map[string]any{
		"seed":   99,
		"agents": []string{"random", "random", "random", "random"},
	})

	progressed := waitFor(t, 3*time.Second, func() bool {
		view := getState(t, client, ts.URL, gameID, 0)
		seq, _ := view["seq"].(float64)
		return seq >= 10
	})
	if !progressed {
		t.Fatal("AI 座位未能自动推进对局")
	}

	status, metrics := doJSON(t, client, http.MethodGet,
		ts.URL+"/api/v1/games/"+gameID+"/metrics", mustToken(t, 0), nil)
	if status != http.StatusOK {
		t.Fatalf("metrics status=%d body=%v", status, metrics)
	}
	if decisions, _ := metrics["agent_decisions"].(float64); decisions <= 0 {
		t.Fatalf("agent_decisions=%v 应 > 0", metrics["agent_decisions"])
	}
	latency, _ := metrics["decision_latency"].(map[string]any)
	if count, _ := latency["count"].(float64); count <= 0 {
		t.Fatalf("decision_latency=%v", latency)
	}
}

// TestConcurrentCreate200 200 并发建局压测：0 错误、id 唯一、房间数正确。
func TestConcurrentCreate200(t *testing.T) {
	srv, ts := newTestServer(t)
	token := mustToken(t, 0)
	transport := ts.Client().Transport

	const total = 200
	var wg sync.WaitGroup
	errs := make(chan error, total)
	ids := make(chan string, total)

	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			payload, err := json.Marshal(map[string]any{
				"seed":   index,
				"agents": []string{"human", "random", "random", "random"},
			})
			if err != nil {
				errs <- fmt.Errorf("marshal: %w", err)
				return
			}
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/games", bytes.NewReader(payload))
			if err != nil {
				errs <- fmt.Errorf("new request: %w", err)
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := transport.RoundTrip(req)
			if err != nil {
				errs <- fmt.Errorf("建局 %d 请求失败: %w", index, err)
				return
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- fmt.Errorf("建局 %d 读取响应失败: %w", index, err)
				return
			}
			if resp.StatusCode != http.StatusCreated {
				errs <- fmt.Errorf("建局 %d status=%d body=%s", index, resp.StatusCode, raw)
				return
			}
			var decoded struct {
				GameID string `json:"game_id"`
			}
			if err := json.Unmarshal(raw, &decoded); err != nil {
				errs <- fmt.Errorf("建局 %d 响应解析失败: %w", index, err)
				return
			}
			if decoded.GameID == "" {
				errs <- fmt.Errorf("建局 %d 缺少 game_id", index)
				return
			}
			ids <- decoded.GameID
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatalf("并发建局失败: %v", err)
	}
	seen := make(map[string]struct{}, total)
	for gameID := range ids {
		if _, ok := seen[gameID]; ok {
			t.Fatalf("game_id 重复: %s", gameID)
		}
		seen[gameID] = struct{}{}
	}
	if len(seen) != total {
		t.Fatalf("成功建局数=%d want %d", len(seen), total)
	}
	if got := srv.Manager().ActiveCount(); got != total {
		t.Fatalf("ActiveCount=%d want %d", got, total)
	}
}

// assertErrorCode 断言错误信封里的 code。
func assertErrorCode(t *testing.T, body map[string]any, want string) {
	t.Helper()
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("响应缺少 error 字段: %v", body)
	}
	if errObj["code"] != want {
		t.Fatalf("错误 code=%v want %s（message=%v）", errObj["code"], want, errObj["message"])
	}
	if _, ok := errObj["message"].(string); !ok {
		t.Fatalf("错误 message 不是字符串: %v", errObj)
	}
}
