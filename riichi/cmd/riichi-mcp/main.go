// Command riichi-mcp 是日麻规则工具层 MCP server（stdio）。
//
// 协议：JSON-RPC 2.0，stdin/stdout 一行一个消息（MCP stdio 传输）。
// 支持的方法：initialize / notifications/initialized / ping / tools/list / tools/call。
//
// 工具（5 个）：mahjong.shanten / mahjong.ukeire / mahjong.safety /
// mahjong.score / mahjong.yaku_potential。
//
// 设计约束（task-10）：
//   - 纯标准库手写协议，不引 MCP SDK 等重依赖；
//   - 工具 handler 都是纯函数（rules/tile 计算），不联网、无状态；
//   - 参数/业务校验错误用 JSON-RPC -32602（invalid params），内部错误用 -32603。
package main

import (
	"log"
	"os"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("riichi-mcp: %v", err)
	}
}

// run 把进程 stdin/stdout 接到 Server 上；单独拆出来便于 main 只保留退出码逻辑。
func run() error {
	return NewServer(os.Stdin, os.Stdout, os.Stderr).Serve()
}
