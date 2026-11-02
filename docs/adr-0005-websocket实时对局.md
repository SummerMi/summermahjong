# ADR-0005：WebSocket 多人实时对局

- 状态：已采纳
- 日期：2026-11-02
- 决策者：工程 B（WebSocket 实时对局层）
- 相关文件：`app/services/realtime/manager.py`、`app/api/routes/ws.py`、
  `app/api/router.py`（挂载 `/api/v1`）、`tests/test_ws.py`、
  依赖已有的 `app/core/security.py`（JWT 座位令牌）与 `app/services/repository/**`
  （`GameRepository` 抽象，HTTP 与 WS 共用同一份对局状态）

---

## 1. 背景

HTTP 版对局接口是「请求-响应」模型：客户端要知道「对手打了什么牌 / 现在轮到谁」，
只能自己轮询 `GET /api/v1/games/{id}`。轮询能跑通，但代价是：

- 每桌 4 个玩家 × 每秒 1 次轮询 = 每秒 4 次无意义请求，绝大部分返回「没有变化」；
- 状态变化有延迟（最多一个轮询周期），打牌动画会顿；
- 服务端连接数随在线人数线性增长，但每次都要重新走鉴权、路由、序列化。

本 ADR 增加 **WebSocket 长连接**：服务端在状态变化时主动推送，把「拉」变成「推」。
实现保持最小：单进程内存连接表 + 广播；跨实例扩展路径写在 §6。

## 2. 决策一：为什么用 WebSocket，而不是轮询 / SSE

| 方案 | 优点 | 为什么不选 / 选 |
|---|---|---|
| **短轮询** | 实现最简单、无状态、天然适配多实例 | 延迟高、请求量浪费；demo 演示「实时」效果差 |
| **长轮询（long polling）** | 兼容性最好（HTTP/1.1 即可） | 每次推送都要重新建请求；服务端要挂起大量请求，连接管理复杂度和 WS 差不多但语义更绕 |
| **SSE（Server-Sent Events）** | 单向推送、基于 HTTP、自动重连 | 只能服务端 → 客户端，打牌是**双向**交互，还得再配一个 POST 接口；浏览器对并发 SSE 有限制 |
| **WebSocket（选它）** | 全双工、低延迟、一条 TCP 连接收发双工；面试常考 | 需要自己管连接 / 心跳 / 重连 / 广播；跨实例要 pub/sub |

**结论**：这是「多人 + 双向 + 高频小消息」场景，WS 是标准解。代价（连接管理、
心跳、背压、跨实例广播）在 §3–§6 逐条写清。

## 3. 决策二：单进程内存连接表

```python
class ConnectionManager:
    _connections: dict[str, list[Connection]]  # game_id -> [Connection(seat, websocket)]
    _lock: asyncio.Lock
```

- **按 `game_id` 分组**：广播天然只要扫一桌，不需要「全服遍历再过滤」；
- **连接记录 `seat`**：断线时能算出「谁的座位掉线了」，推送 presence；
- **`asyncio.Lock` 保护增删**：`connect` / `disconnect` / 广播前拍快照都在锁内；
  广播**先在锁内 `list(...)` 快照、再在锁外逐条 await 发送**——如果边发边持锁，
  一个慢客户端就会把整桌的连接表锁住，别人连断线都登记不了；
- **发送失败自动摘除**：客户端网络断了但服务端还没收到 close 帧时，
  `send_text` 会抛异常；逐条 `try/except` 把它从表里摘掉，一个坏连接不影响其他玩家；
- **挂在 `app.state.ws_manager`**（首次连接惰性创建）：每个 `create_app()` 实例
  独立，测试之间不共享全局状态。没有放 `main.py` 是因为本任务的改动边界不包含
  `main.py`；后续若要支持优雅关闭（`lifespan` 退出时统一 close），把创建移到
  lifespan 即可，路由代码不用改；
- **与 HTTP 共用 `store`**：WS 打牌走同一个 `GameRepository.discard`，所以
  「WS 打牌 → HTTP 查询」立刻能看到新状态（`tests/test_ws.py` 有专门用例），
  三存储（内存 / SQL / Redis）与 cache-aside 自动生效，不需要为 WS 复制一套状态。

**为什么不在连接表里存整局状态**：状态只有一份真相（`GameRepository`），
连接表只做「谁在线、怎么发消息」。否则内存里会有两份状态，迟早不一致。

## 4. 决策三：心跳与断线检测

浏览器/移动端的 TCP 连接可能「假死」：拔网线、手机切后台、进程被杀，服务端
可能长时间收不到 FIN/RST，`onclose` 永远不触发。所以需要心跳。

本实现是两层：

1. **客户端主动 ping / 服务端 pong**（应用层，浏览器 JS 定时器）：
   `{"type":"ping","ts":123}` → `{"type":"pong","ts":123}`，客户端按 RTT 判断链路，
   超时自己重连；
2. **服务端空闲兜底**：`_receive_loop` 用
   `asyncio.wait_for(websocket.receive_text(), timeout=IDLE_TIMEOUT_SECONDS=60)`，
   60s 内没收到**任何**客户端消息就 `close(code=1000, reason="idle timeout")`。
   60s 留出客户端丢一两次心跳的余量（客户端建议 30s 一次）。

为什么是「收到任何消息」而不是只认 ping：打牌消息本身就是活着的证据，不必强制
客户端在出牌时还发心跳。生产更严谨的做法是服务端周期发 ping 帧 + 等 pong（
`uvicorn --ws-ping-interval` 也提供底层 TCP 级 ping），本实现保持 demo 简单。

客户端被服务端关闭后应 **指数退避重连**，重连时带 token 重新握手（本实现无
resume 机制，重连后重新 `get_state` 拉全量即可——状态本来就在服务端）。

## 5. 决策四：握手鉴权与 close code 约定

WebSocket 握手是 HTTP Upgrade，但**浏览器的 WebSocket API 不能自定义请求头**，
没法带 `Authorization: Bearer`。所以选 `?token=<JWT>` 查询参数（另一种主流做法是
「连接建立后第一条消息发 token」，延迟鉴权、实现更绕）。

- token 会进 access log / 浏览器历史：生产要**日志脱敏**，或改为签发短期一次性
  ticket（HTTP 接口换 ticket → WS 用 ticket 握手，ticket 一次性且 30s 过期）；
- 校验内容复用 HTTP 侧同一套 `decode_access_token`（过期 / 签名 / 用途 / 座位），
  并额外要求 `token.sub == ?seat`，否则 4403；
- 握手阶段拒绝用 `websocket.close(code=...)`（`accept()` 之前），客户端会收到
  close 帧而不是 HTTP 401。

| close code | 含义 |
|---|---|
| 4400 | `seat` 非法（必须是 east/south/west/north） |
| 4401 | 启用 JWT 时 token 缺失 / 无效 / 过期 |
| 4403 | token 的座位与 `seat` 参数不一致（座位越权） |
| 4404 | 对局不存在（或已被 TTL 清理 / 上限淘汰） |
| 1000 | 正常关闭；服务端空闲超时也用它（客户端据此重连） |
| 1011 | 内部错误（应用未初始化等） |

**校验顺序**：seat 格式（4400）→ 鉴权（4401/4403）→ 对局存在（4404）。
先验身份再查资源，避免未授权调用方拿 close code 当「对局是否存在」的探测器。

## 6. 扩展路径：多实例为什么必须 Redis pub/sub（本实现未做）

当前连接表在进程内存里，**只能单实例**：

```
玩家 A → 实例 1（WS 连接在实例 1）
玩家 B → 实例 2（WS 连接在实例 2）
A 打牌 → 实例 1 的 store 更新 + 实例 1 广播 → B 收不到任何东西
```

注意即使把对局状态放到 Redis（ADR-0003），**连接表仍然在各自进程里**——状态一致
不等于消息可达。多实例必须加一层「跨实例消息总线」：

1. **Redis pub/sub（最小改动）**：每个实例订阅 `ws:game:{game_id}` 频道；
   本地打牌成功后 `PUBLISH` 一条 `state` 消息，所有实例收到后广播给**自己**的连接。
   pub/sub 是「发后即忘」，实例短暂掉线会丢消息 → 客户端靠重连 + `get_state` 兜底；
2. **Redis Stream / Kafka（更稳）**：需要「不丢消息 / 可回放 / 消费组」时用流；
   代价是要管理 consumer group、offset、重复消费幂等；
3. **粘性会话（sticky session）**：让同一桌玩家都路由到同一实例（按 `game_id`
   哈希）。实现最省事，但扩缩容会抖动、热门桌仍然单点，只适合临时方案；
4. **独立推送服务**：网关只维护连接、业务服务发事件到消息队列，网关消费后推送
   （微信/游戏后端常见形态）。

另外两个跨实例问题：

- **多实例的连接数上限**需要集中统计（否则每实例都以为自己没满）；
- **在线座位 presence** 目前是本地快照，多实例时要聚合（Redis Set + 实例心跳）。

## 7. 决策五：消息协议

客户端 → 服务端：

| 消息 | 语义 |
|---|---|
| `{"type":"ping","ts":123}` | 心跳，服务端回 `pong` 原样回显 ts |
| `{"type":"discard","tile":"2m"}` | 出牌；座位由**连接**决定，消息里不允许指定座位（避免越权） |
| `{"type":"get_state"}` | 拉全量状态（重连后 / 客户端状态丢失时） |

服务端 → 客户端：

| 消息 | 说明 |
|---|---|
| `{"type":"connected","game_id","seat","wall_remaining"}` | 连接成功第一条 |
| `{"type":"presence","seats_online":[...]}` | 有人加入 / 离开时广播全桌 |
| `{"type":"state","wall_remaining","current_player","discards","last"}` | 有人打牌后广播；`last` 是刚打出的牌，便于前端做动画 |
| `{"type":"pong","ts"}` | 心跳响应 |
| `{"type":"error","code","message"}` | **只发给触发方**，连接不断开 |

设计取舍：

- **错误不断线**：长连接重连成本高，非法 JSON / 未知 type / 业务错误（牌不在手、
  没轮到你）都应可恢复；
- **discard 不指定座位**：身份来自已鉴权的连接，消息里再带 seat 只会多一条越权面；
- **状态消息带全量牌河而不是增量**：一桌 4 人、数据量很小，全量最不容易出现
  「客户端漏一条增量后永久错位」；真正的大项目可用「版本号 + 增量」并做补拉。

## 8. 决策六：背压与慢客户端

`broadcast` 目前逐条 `await send_text`，最慢的连接会拖慢同一轮广播：

- **demo 规模可接受**：一桌 4 人、消息是几百字节的 JSON；
- **生产做法**：每连接一个有界 `asyncio.Queue`（如 32 条），广播只做 `put_nowait`；
  队列满说明客户端消费不过来 → 丢弃非关键消息（presence/state 可丢，用 get_state
  兜底）或直接断开慢客户端；
- 另一种是 `asyncio.gather(..., return_exceptions=True)` + 单条发送超时，
  避免串行等待，但会放大瞬时并发；
- **本实现已做的最低限度**：发送失败（含慢到超时/断连）自动摘除，不阻塞其他人；
  但**没有**队列与主动丢弃——这一点在面试里要主动说明，别假装做了。

## 9. 测试覆盖（`tests/test_ws.py`，21 个用例）

- 连接：`connected` 字段、`presence` 单人 / 双人、在线座位按东南西北排序；
- 广播：两个客户端都收到 `state`（墙 83→82、牌河 +1、`last` 正确）、
  两局之间广播隔离、`get_state` 只回请求者；
- 一致性：WS 打牌后 HTTP `GET /api/v1/games/{id}` 立刻可见（共用同一 store）；
- 心跳/错误：ping→pong 回显、非法 JSON / 未知 type / 缺 tile 回 `error` 且不断线、
  业务错误只回发送者（另一位玩家随后 ping 收到 pong，证明没有误广播）；
- 断线：`presence` 更新、`connection_count` 归零、`seats_online` 清空；
- 空闲超时：monkeypatch 把 `IDLE_TIMEOUT_SECONDS` 调到 0.2s，断言 close 1000；
- 握手拒绝：4400（seat 非法）、4404（对局不存在）、4401（无 token / 非法 token /
  过期 token）、4403（token 座位不匹配）、合法 token 可连接。

## 10. 面试话术（3–5 句）

1. 「实时对局本质是**推送**问题，我们用 WebSocket 把轮询换成服务端主动广播，
   状态变化延迟从『一个轮询周期』降到毫秒级，也省掉了大量空轮询请求。」
2. 「连接表按 `game_id` 分组、用 `asyncio.Lock` 保护增删；广播先在锁内拍快照再在
   锁外逐条发送——锁只保护结构，不保护网络 IO，否则一个慢客户端会锁住整桌。」
3. 「断线检测靠两层心跳：客户端 ping/pong 算 RTT，服务端 60s 没收到任何消息就
   close 1000，处理『TCP 假死、onclose 不触发』的情况；客户端指数退避重连后
   用 `get_state` 拉全量，不需要服务端保存增量。」
4. 「WS 鉴权用 query token，因为浏览器 WebSocket API 不能自定义 header；
   生产会换成短期一次性 ticket + 日志脱敏，避免 token 进访问日志。」
5. 「当前是单进程内存连接表，多实例必须加 Redis pub/sub：每个实例订阅本局频道、
   本地广播给自家连接；pub/sub 会丢消息，所以客户端重连后 `get_state` 兜底。
   慢客户端则用有界发送队列 + 丢弃/断开，不能让背压拖垮整桌。」
