package main

import (
	"encoding/json"
	"fmt"
)

// jsonrpcVersion 是 JSON-RPC 2.0 的固定版本号。
const jsonrpcVersion = "2.0"

// JSON-RPC 2.0 标准错误码（MCP 直接沿用这套）。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// rpcRequest 是一条进来的 JSON-RPC 请求（或通知：没有 id）。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification 报告这条消息是否不需要响应（JSON-RPC 通知 = 没有 id）。
func (r rpcRequest) isNotification() bool { return len(r.ID) == 0 }

// rpcResponse 是一条出去的 JSON-RPC 响应。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError 是 JSON-RPC 错误对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("jsonrpc error %d: %s", e.Code, e.Message)
}

// toolError 表示工具参数 / 业务校验失败，统一映射为 -32602 invalid params。
//
// 与内部错误（-32603）分开，是为了让 MCP 客户端能区分「我传错了」和「服务端 bug」。
type toolError struct{ msg string }

func (e *toolError) Error() string { return e.msg }

// invalidParams 构造一个参数错误。
func invalidParams(format string, args ...any) error {
	return &toolError{msg: fmt.Sprintf(format, args...)}
}
