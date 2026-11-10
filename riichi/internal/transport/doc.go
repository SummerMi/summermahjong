// Package transport 实现 riichi 对局服务的 HTTP / WebSocket 传输层（S6）。
//
// 边界（契约 §1）：本包只依赖 game / store / agent，向下不反向依赖；规则与状态机
// 全部在 internal/game，本包不实现任何麻将规则。
//
// 分层：
//
//	auth.go      JWT（HS256，stdlib crypto/hmac）签发与校验，座位绑定
//	syncstore.go 给 store.Store 套互斥锁（Memory 实现非并发安全；SQL/Redis 实现同样安全）
//	wire.go      协议结构体：WS 消息、事件 payload 富化、Observation → HTTP JSON
//	room.go      Room = 单 goroutine actor：串行 Apply + 落库 + 广播 + AI 驱动
//	manager.go   RoomManager：建局/查询/关闭，200 并发建局安全
//	server.go    HTTP handler + WS handler + 路由 + 错误映射
//	metrics.go   进程级与单局指标（决策耗时 P50/P95、决策次数、断线重连次数等）
//
// 协议（契约 §6）：
//
//	HTTP
//	  POST /api/v1/games                  建局（需 JWT）
//	  GET  /api/v1/games/{id}             当前状态，按 JWT 座位裁剪（game.Observe）
//	  GET  /api/v1/games/{id}/replay      全量事件（回放）
//	  GET  /api/v1/games/{id}/metrics     单局指标
//	  GET  /metrics                       进程指标
//	  GET  /healthz                       健康检查
//
//	WebSocket /api/v1/ws/games/{id}?seat=&token=
//	  客户端 → 服务端: {"type":"resume","last_seq":N} / {"type":"action",...} / {"type":"ping","ts":...}
//	  服务端 → 客户端: {"type":"event",...} / {"type":"resumed",...} / {"type":"pong",...} / {"type":"error",...}
//
// 断线续传语义（契约 §6.2）：
//
//   - 连接建立后**不**自动回放历史事件；客户端发 resume(last_seq) 触发补齐，
//     服务端用 store.LoadEvents(last_seq) 只发 seq > last_seq 的事件，然后回 resumed。
//     新客户端用 resume(0) 拿全量事件。这样「重连补齐」天然不丢不重；
//   - 未完成 resume 的连接不接收实时广播（事件已落库，resume 时会补齐），
//     避免「先收到 live 事件、resume 又收到一遍」的重复；
//   - 同一连接上重复 resume 时，服务端按「已发送的最大 seq」跳过重复事件。
//
// 事件 payload 富化：M1 的 game.Event 只带 Tile/TileRed，本包在 Apply 之后按契约
// §2.2 给事件补 payload（draw: tile/wall_remaining；discard: tile/tsumogiri/
// riichi_declared；round_start: round_wind/kyoku/honba/dealer/scores），并把富化后的
// 事件写入 store，因此 replay / 续传拿到的事件与实时事件逐字段一致。
//
// M1 限制：只有摸打，没有吃碰杠/立直/和了；事件按全量广播（含摸牌），尚未按座位做
// 事件过滤（M2 扩展，见 docs/riichi-接口契约-20261110.md §2.2）。
package transport
