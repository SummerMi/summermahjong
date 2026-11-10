package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
)

const (
	serverName    = "riichi-mcp"
	serverVersion = "0.1.0"

	// defaultProtocolVersion 是 MCP 的稳定协议版本；客户端传了版本就回显客户端的，
	// 兼容 2024-11-05 与后续小版本（tools 能力本身没变）。
	defaultProtocolVersion = "2024-11-05"

	// maxLineBytes 是单条 JSON-RPC 消息上限（1 MiB，防超长输入把内存打爆）。
	maxLineBytes = 1 << 20
)

// Server 是 stdio 的 MCP server：读一行 JSON-RPC、写一行响应。
type Server struct {
	scanner  *bufio.Scanner
	enc      *json.Encoder
	log      *log.Logger
	tools    map[string]tool
	toolList []tool
}

// NewServer 构造 server；in/out/errOut 由调用方注入，测试里可以换成 io.Pipe。
func NewServer(in io.Reader, out io.Writer, errOut io.Writer) *Server {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	tools := defaultTools()
	index := make(map[string]tool, len(tools))
	for _, t := range tools {
		index[t.Name] = t
	}
	return &Server{
		scanner:  scanner,
		enc:      json.NewEncoder(out),
		log:      log.New(errOut, "riichi-mcp: ", 0),
		tools:    index,
		toolList: tools,
	}
}

// Serve 循环处理 stdin 直到 EOF（MCP stdio 下客户端关闭管道即退出）。
func (s *Server) Serve() error {
	for s.scanner.Scan() {
		line := bytes.TrimSpace(s.scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		s.handleLine(line)
	}
	return s.scanner.Err()
}

// handleLine 处理一条 JSON-RPC 消息（请求或通知）。
func (s *Server) handleLine(line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeError(nil, &rpcError{Code: codeParseError, Message: "解析 JSON 失败: " + err.Error()})
		return
	}
	if req.JSONRPC != jsonrpcVersion || req.Method == "" {
		// 通知即使格式不对也不该回响应；请求才回 -32600。
		if req.isNotification() {
			return
		}
		s.writeError(req.ID, &rpcError{Code: codeInvalidRequest, Message: "不是合法的 JSON-RPC 2.0 请求"})
		return
	}
	if req.isNotification() {
		s.handleNotification(req)
		return
	}
	result, rerr := s.dispatch(req)
	if rerr != nil {
		s.writeError(req.ID, rerr)
		return
	}
	s.writeResult(req.ID, result)
}

// handleNotification 处理 JSON-RPC 通知（MCP 的 initialized / cancelled 等），不回包。
func (s *Server) handleNotification(req rpcRequest) {
	switch req.Method {
	case "notifications/initialized", "notifications/cancelled":
		// 无状态 server：握手通知与取消通知都不需要动作。
	default:
		s.log.Printf("忽略未知通知: %s", req.Method)
	}
}

// dispatch 按 method 分发；返回值或 JSON-RPC 错误。
func (s *Server) dispatch(req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params)
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return s.toolsList(), nil
	case "tools/call":
		return s.callTool(req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("未知方法: %s", req.Method)}
	}
}

// initialize 实现 MCP 握手：返回协议版本、tools 能力与 serverInfo。
func (s *Server) initialize(params json.RawMessage) (any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 && !bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "initialize 参数格式错误: " + err.Error()}
		}
	}
	protocol := p.ProtocolVersion
	if protocol == "" {
		protocol = defaultProtocolVersion
	}
	return map[string]any{
		"protocolVersion": protocol,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{"name": serverName, "version": serverVersion},
		"instructions": "日麻（立直麻将）规则工具：向听 / 有效进张 / 危险度 / 役种与点数。" +
			"牌面编码：1m..9m、1p..9p、1s..9s、东 E、南 S、西 W、北 N、白 P、发 F、中 C；赤 5 写作 r5m。",
	}, nil
}

// toolSchema 是 tools/list 里单个工具的声明。
type toolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolsList 返回 5 个工具及其 JSON Schema。
func (s *Server) toolsList() any {
	schemas := make([]toolSchema, 0, len(s.toolList))
	for _, t := range s.toolList {
		schemas = append(schemas, toolSchema{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return map[string]any{"tools": schemas}
}

// callTool 执行一次工具调用，结果封成 MCP 的 content 列表。
func (s *Server) callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call 缺少 params"}
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call 参数格式错误: " + err.Error()}
	}
	t, ok := s.tools[p.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "未知工具: " + p.Name}
	}
	args := p.Arguments
	if len(args) == 0 || bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
		args = json.RawMessage(`{}`)
	}
	result, err := t.Handler(context.Background(), args)
	if err != nil {
		var te *toolError
		if errors.As(err, &te) {
			return nil, &rpcError{Code: codeInvalidParams, Message: te.msg}
		}
		return nil, &rpcError{Code: codeInternalError, Message: err.Error()}
	}
	text, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return nil, &rpcError{Code: codeInternalError, Message: "序列化工具结果失败: " + err.Error()}
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(text)}},
		"structuredContent": result,
		"isError":           false,
	}, nil
}

// writeResult 写一条成功响应。
func (s *Server) writeResult(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: jsonrpcVersion, ID: idOrNull(id), Result: result})
}

// writeError 写一条错误响应。
func (s *Server) writeError(id json.RawMessage, rerr *rpcError) {
	s.write(rpcResponse{JSONRPC: jsonrpcVersion, ID: idOrNull(id), Error: rerr})
}

// write 编码并输出一行 JSON；写失败只记日志（stdio 断了对端也收不到更多）。
func (s *Server) write(resp rpcResponse) {
	if err := s.enc.Encode(resp); err != nil {
		s.log.Printf("写响应失败: %v", err)
	}
}

// idOrNull 把空 id 归一化成 JSON null（JSON-RPC 解析错误时要求 id: null）。
func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}
