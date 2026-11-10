package main

import (
	"bufio"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// session 用 io.Pipe 模拟 MCP stdio：测试进程是客户端，Server 在另一端读写。
type session struct {
	in      *io.PipeWriter
	out     *bufio.Reader
	done    chan error
	closing bool
}

// newSession 启动一个 Server 并把 stdin/stdout 接到管道上。
func newSession(t *testing.T) *session {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	server := NewServer(inReader, outWriter, io.Discard)
	done := make(chan error, 1)
	go func() {
		done <- server.Serve()
		_ = outWriter.Close()
	}()
	s := &session{in: inWriter, out: bufio.NewReader(outReader), done: done}
	t.Cleanup(func() { s.close(t) })
	return s
}

// close 关闭输入管道并等待 Serve 退出（Serve 在 EOF 时返回 nil）。
func (s *session) close(t *testing.T) {
	t.Helper()
	if s.closing {
		return
	}
	s.closing = true
	_ = s.in.Close()
	select {
	case err := <-s.done:
		if err != nil {
			t.Logf("server exited with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Log("server did not exit after stdin closed")
	}
}

// writeLine 写一行原始 JSON。
func (s *session) writeLine(t *testing.T, line string) {
	t.Helper()
	if _, err := s.in.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// readResponse 读一行响应并解析。
func (s *session) readResponse(t *testing.T) map[string]any {
	t.Helper()
	line, err := s.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	return response
}

// request 发送一条 JSON-RPC 请求并读响应。
func (s *session) request(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": nextID(), "method": method}
	if params != nil {
		request["params"] = params
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	s.writeLine(t, string(payload))
	return s.readResponse(t)
}

// callTool 调一个工具并返回解析后的 structuredContent（同时校验没有 JSON-RPC 错误）。
func (s *session) callTool(t *testing.T, name string, args any) map[string]any {
	t.Helper()
	response := s.request(t, "tools/call", map[string]any{"name": name, "arguments": args})
	return toolResult(t, response)
}

var requestIDCounter int

func nextID() int {
	requestIDCounter++
	return requestIDCounter
}

// resultMap 断言响应成功并返回 result 对象（initialize / ping / tools/list 用）。
func resultMap(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	if rpcErr, ok := response["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", rpcErr)
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", response)
	}
	return result
}

// toolResult 从 tools/call 响应里取 structuredContent。
func toolResult(t *testing.T, response map[string]any) map[string]any {
	t.Helper()
	result := resultMap(t, response)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tool returned isError: %v", result)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("missing structuredContent: %v", result)
	}
	return structured
}

// errorCode 断言响应是 JSON-RPC 错误，返回 code。
func errorCode(t *testing.T, response map[string]any) float64 {
	t.Helper()
	rpcErr, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected JSON-RPC error, got %v", response)
	}
	code, ok := rpcErr["code"].(float64)
	if !ok {
		t.Fatalf("error has no numeric code: %v", rpcErr)
	}
	return code
}

// ── 协议层 ───────────────────────────────────────────────────────────────

func TestInitializeHandshake(t *testing.T) {
	s := newSession(t)
	response := s.request(t, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "0.0.1"},
	})
	result := resultMap(t, response) // initialize 的 result 也是 map
	if got := result["protocolVersion"]; got != "2025-03-26" {
		t.Fatalf("protocolVersion = %v, want echo of client version", got)
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != serverName {
		t.Fatalf("serverInfo.name = %v", serverInfo["name"])
	}
	capabilities, _ := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("capabilities.tools missing: %v", capabilities)
	}
}

func TestInitializeWithoutParamsUsesDefaultVersion(t *testing.T) {
	s := newSession(t)
	result := resultMap(t, s.request(t, "initialize", nil))
	if result["protocolVersion"] != defaultProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %v", result["protocolVersion"], defaultProtocolVersion)
	}
}

func TestPing(t *testing.T) {
	s := newSession(t)
	result := resultMap(t, s.request(t, "ping", nil))
	if len(result) != 0 {
		t.Fatalf("ping result should be empty, got %v", result)
	}
}

func TestToolsListDeclaresAllToolsWithSchema(t *testing.T) {
	s := newSession(t)
	result := resultMap(t, s.request(t, "tools/list", nil))
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("tools missing: %v", result)
	}
	if len(tools) != 5 {
		t.Fatalf("got %d tools, want 5", len(tools))
	}
	wantNames := map[string]bool{
		"mahjong.shanten": false, "mahjong.ukeire": false, "mahjong.safety": false,
		"mahjong.score": false, "mahjong.yaku_potential": false,
	}
	for _, raw := range tools {
		toolDef, _ := raw.(map[string]any)
		name, _ := toolDef["name"].(string)
		if _, ok := wantNames[name]; !ok {
			t.Fatalf("unexpected tool %q", name)
		}
		wantNames[name] = true
		schema, _ := toolDef["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("tool %s inputSchema.type = %v", name, schema["type"])
		}
		if description, _ := toolDef["description"].(string); description == "" {
			t.Fatalf("tool %s has empty description", name)
		}
	}
	for name, seen := range wantNames {
		if !seen {
			t.Fatalf("tool %s not listed", name)
		}
	}
}

func TestNotificationProducesNoResponse(t *testing.T) {
	s := newSession(t)
	// 通知不该有响应；紧接着的 ping 响应必须是我们读到的第一条消息。
	s.writeLine(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	ping := s.request(t, "ping", nil)
	if ping["error"] != nil {
		t.Fatalf("ping failed: %v", ping)
	}
}

func TestParseErrorRecoversAndResponds(t *testing.T) {
	s := newSession(t)
	s.writeLine(t, `{"jsonrpc": "2.0", "id": 7, "method": `) // 非法 JSON
	response := s.readResponse(t)
	if code := errorCode(t, response); code != codeParseError {
		t.Fatalf("code = %v, want %v", code, codeParseError)
	}
	if response["id"] != nil {
		t.Fatalf("parse error id = %v, want null", response["id"])
	}
	// 解析错误后 server 仍能继续服务后续请求。
	if ping := s.request(t, "ping", nil); ping["error"] != nil {
		t.Fatalf("server did not recover after parse error: %v", ping)
	}
}

func TestUnknownMethodAndInvalidRequest(t *testing.T) {
	s := newSession(t)
	if code := errorCode(t, s.request(t, "no/such/method", nil)); code != codeMethodNotFound {
		t.Fatalf("unknown method code = %v, want %v", code, codeMethodNotFound)
	}
	s.writeLine(t, `{"id": 3, "method": "ping"}`) // 缺 jsonrpc 版本
	if code := errorCode(t, s.readResponse(t)); code != codeInvalidRequest {
		t.Fatalf("invalid request code = %v, want %v", code, codeInvalidRequest)
	}
}

// ── 工具成功路径 ─────────────────────────────────────────────────────────

func TestShantenTool(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.shanten", map[string]any{
		"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
	})
	if result["shanten"] != float64(0) || result["tenpai"] != true {
		t.Fatalf("shanten result = %v", result)
	}
	if result["meld_count"] != float64(0) {
		t.Fatalf("meld_count = %v", result["meld_count"])
	}
}

func TestShantenToolWithMelds(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.shanten", map[string]any{
		"hand":      []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p"},
		"meldCount": 1,
	})
	if result["meld_count"] != float64(1) {
		t.Fatalf("meld_count = %v", result["meld_count"])
	}
	if shanten, _ := result["shanten"].(float64); shanten < 0 {
		t.Fatalf("unexpected shanten: %v", result["shanten"])
	}
}

func TestUkeireTool(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.ukeire", map[string]any{
		"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
	})
	tiles, ok := result["tiles"].([]any)
	if !ok || len(tiles) == 0 {
		t.Fatalf("ukeire tiles missing: %v", result)
	}
	remaining := make(map[string]float64, len(tiles))
	for _, raw := range tiles {
		entry, _ := raw.(map[string]any)
		name, _ := entry["tile"].(string)
		count, _ := entry["remaining"].(float64)
		remaining[name] = count
	}
	// 123m456m789m + 11p + 23p 听 1p（剩 2 张）和 4p（剩 4 张）。
	if remaining["1p"] != 2 {
		t.Fatalf("1p remaining = %v, want 2 (all=%v)", remaining["1p"], remaining)
	}
	if remaining["4p"] != 4 {
		t.Fatalf("4p remaining = %v, want 4 (all=%v)", remaining["4p"], remaining)
	}
	if total, _ := result["total"].(float64); total < 6 {
		t.Fatalf("total ukeire = %v, want >= 6", result["total"])
	}
}

func TestSafetyToolSortsByDanger(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.safety", map[string]any{
		"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p", "4p"},
		"discards": [][]string{
			{}, {}, {"1p", "9s"}, {},
		},
		"riichi": []bool{false, false, true, false},
	})
	candidates, ok := result["candidates"].([]any)
	if !ok || len(candidates) == 0 {
		t.Fatalf("candidates missing: %v", result)
	}
	first, _ := candidates[0].(map[string]any)
	if first["tile"] != "1p" {
		t.Fatalf("first candidate = %v, want 现物 1p", first["tile"])
	}
	if danger, _ := first["danger"].(float64); danger != 0 {
		t.Fatalf("1p danger = %v, want 0", first["danger"])
	}
	riichiSeats, _ := result["riichi_seats"].([]any)
	if len(riichiSeats) != 1 || riichiSeats[0] != float64(2) {
		t.Fatalf("riichi_seats = %v", result["riichi_seats"])
	}
}

func TestSafetyToolWithoutRiichi(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.safety", map[string]any{
		"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p", "4p"},
	})
	nodes, _ := result["candidates"].([]any)
	for _, raw := range nodes {
		entry, _ := raw.(map[string]any)
		if danger, _ := entry["danger"].(float64); danger != 0 {
			t.Fatalf("danger without riichi should be 0, got %v", entry)
		}
	}
	if note, _ := result["note"].(string); note == "" {
		t.Fatalf("expected note when no riichi: %v", result)
	}
}

func TestScoreTool(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.score", map[string]any{
		"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
		"winning": "4p",
		"ctx":     map[string]any{"dora_indicators": []string{"3p"}},
	})
	if result["pattern"] != "standard" {
		t.Fatalf("pattern = %v", result["pattern"])
	}
	if result["has_yaku"] != true {
		t.Fatalf("has_yaku = %v", result["has_yaku"])
	}
	// 平和 1 翻 + 宝牌 1 翻（指示牌 3p → 4p 在手里 1 张）
	if result["dora"] != float64(1) {
		t.Fatalf("dora = %v, want 1", result["dora"])
	}
	if total, _ := result["total_han"].(float64); total < 2 {
		t.Fatalf("total_han = %v, want >= 2", result["total_han"])
	}
	points, _ := result["points"].(map[string]any)
	if total, _ := points["total"].(float64); total <= 0 {
		t.Fatalf("points.total = %v", points["total"])
	}
}

func TestScoreToolWith14TileHand(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.score", map[string]any{
		"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p", "4p"},
		"winning": "4p",
	})
	if result["pattern"] != "standard" {
		t.Fatalf("14-tile hand should still score: %v", result)
	}
}

func TestScoreToolWithOpenMeld(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.score", map[string]any{
		"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p"},
		"winning": "1p",
		"melds":   []map[string]any{{"kind": "pon", "tiles": []string{"2p", "2p", "2p"}}},
	})
	if result["pattern"] != "standard" {
		t.Fatalf("open hand should decompose: %v", result)
	}
	if result["meld_count"] != float64(1) {
		t.Fatalf("meld_count = %v", result["meld_count"])
	}
	if result["has_yaku"] != true {
		t.Fatalf("open hand 123m456m789m + pon 2p should have 一气通贯: %v", result)
	}
	points, _ := result["points"].(map[string]any)
	if total, _ := points["total"].(float64); total <= 0 {
		t.Fatalf("points.total = %v", points["total"])
	}
}

func TestYakuPotentialTool(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.yaku_potential", map[string]any{
		"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
	})
	waiting, _ := result["waiting_tiles"].([]any)
	if len(waiting) != 2 {
		t.Fatalf("waiting_tiles = %v, want 2 (1p/4p)", waiting)
	}
	yaku, _ := result["yaku"].([]any)
	if len(yaku) == 0 {
		t.Fatalf("yaku_potential should list at least 平和: %v", result)
	}
	foundPinfu := false
	for _, raw := range yaku {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "平和" {
			foundPinfu = true
		}
	}
	if !foundPinfu {
		t.Fatalf("yaku should contain 平和: %v", yaku)
	}
}

func TestYakuPotentialToolNotTenpai(t *testing.T) {
	s := newSession(t)
	result := s.callTool(t, "mahjong.yaku_potential", map[string]any{
		"hand": []string{"1m", "1m", "1m", "4m", "5m", "7p", "7p", "9p", "2s", "2s", "5s", "8s", "N"},
	})
	waiting, _ := result["waiting_tiles"].([]any)
	if len(waiting) != 0 {
		t.Fatalf("waiting_tiles = %v, want empty", waiting)
	}
	if note, _ := result["note"].(string); note == "" {
		t.Fatalf("expected note for non-tenpai hand: %v", result)
	}
}

// ── 工具错误路径 ─────────────────────────────────────────────────────────

func TestToolErrors(t *testing.T) {
	s := newSession(t)
	cases := []struct {
		name   string
		tool   string
		args   any
		want   float64
		reason string
	}{
		{"unknown tool", "mahjong.nope", map[string]any{}, codeInvalidParams, ""},
		{"shanten wrong hand size", "mahjong.shanten", map[string]any{"hand": []string{"1m"}}, codeInvalidParams, ""},
		{"shanten bad tile", "mahjong.shanten", map[string]any{
			"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "9q"},
		}, codeInvalidParams, ""},
		{"shanten bad meld count", "mahjong.shanten", map[string]any{
			"hand":      []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
			"meldCount": 5,
		}, codeInvalidParams, ""},
		{"ukeire wrong hand size", "mahjong.ukeire", map[string]any{"hand": []string{"1m", "2m"}}, codeInvalidParams, ""},
		{"safety bad seat", "mahjong.safety", map[string]any{
			"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p", "4p"},
			"seat": 9,
		}, codeInvalidParams, ""},
		{"safety bad discards", "mahjong.safety", map[string]any{
			"hand":     []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p", "4p"},
			"discards": [][]string{{"9q"}},
		}, codeInvalidParams, ""},
		{"score not winning", "mahjong.score", map[string]any{
			"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
			"winning": "9s",
		}, codeInvalidParams, ""},
		{"score menzen mismatch", "mahjong.score", map[string]any{
			"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
			"winning": "4p",
			"ctx":     map[string]any{"menzen": false},
		}, codeInvalidParams, ""},
		{"score bad meld kind", "mahjong.score", map[string]any{
			"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
			"winning": "4p",
			"melds":   []map[string]any{{"kind": "nope", "tiles": []string{"1m", "2m", "3m"}}},
		}, codeInvalidParams, ""},
		{"score bad meld shape", "mahjong.score", map[string]any{
			"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p"},
			"winning": "1p",
			"melds":   []map[string]any{{"kind": "pon", "tiles": []string{"1m", "1m"}}},
		}, codeInvalidParams, ""},
		{"score bad ctx wind", "mahjong.score", map[string]any{
			"hand":    []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p"},
			"winning": "4p",
			"ctx":     map[string]any{"seat_wind": "1m"},
		}, codeInvalidParams, ""},
		{"yaku potential 14 tiles", "mahjong.yaku_potential", map[string]any{
			"hand": []string{"1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "2p", "3p", "4p"},
		}, codeInvalidParams, ""},
		{"arguments wrong type", "mahjong.shanten", map[string]any{"hand": 123}, codeInvalidParams, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := s.request(t, "tools/call", map[string]any{"name": testCase.tool, "arguments": testCase.args})
			if code := errorCode(t, response); code != testCase.want {
				t.Fatalf("code = %v, want %v (response=%v)", code, testCase.want, response)
			}
		})
	}
}

func TestToolsCallWithoutParamsAndBadParams(t *testing.T) {
	s := newSession(t)
	if code := errorCode(t, s.request(t, "tools/call", nil)); code != codeInvalidParams {
		t.Fatalf("missing params code = %v", code)
	}
	if code := errorCode(t, s.request(t, "tools/call", map[string]any{"name": "mahjong.shanten"})); code != codeInvalidParams {
		t.Fatalf("missing arguments should fail validation, got %v", code)
	}
	if code := errorCode(t, s.request(t, "tools/call", "not-an-object")); code != codeInvalidParams {
		t.Fatalf("non-object params code = %v", code)
	}
}
