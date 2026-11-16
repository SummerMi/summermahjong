# riichi — 日麻多智能体服务（v2）

> 单人对三个 AI 打日麻（天鳳式），支持只读观战；同时是「Go 后端 + Agent」方向的作品集项目。
> 设计文档：[`docs/riichi-架构与里程碑-20261110.md`](../docs/riichi-架构与里程碑-20261110.md) ·
> 接口契约：[`docs/riichi-接口契约-20261110.md`](../docs/riichi-接口契约-20261110.md) ·
> 一页索引：[`docs/riichi-交付清单-20261110.md`](../docs/riichi-交付清单-20261110.md)

## 这是什么

一个 **Go 单体服务**：真实规则引擎（41 役种 + 符 + 点数）+ 事件溯源对局服务（HTTP/WS、断线续传）+ 多策略 AI（13 个工厂名）+ 自对弈评测（指标 + ELO）+ MCP 规则工具层（5 个工具）+ 最小牌桌网页。
实现 11,382 行 / 测试 11,967 行（104 个 `.go` 文件），`tile` 包覆盖率 100%。

## 目录

```
cmd/
  riichi-server/   HTTP + WS 对局服务（JWT 座位绑定、Room actor、真打响应窗口）
  riichi-eval/     进程内自对弈评测 CLI（指标 + 95% CI + Bradley-Terry ELO + 报告）
  riichi-mcp/      MCP 规则工具层（stdio JSON-RPC，5 个工具，纯标准库）
internal/
  tile/            牌、牌山、赤宝牌、王牌、SplitMix64 确定性洗牌（100%）
  rules/           判胡拆解、向听、41 役、符、点数（96.2%）
  game/            状态机：摸打/副露/立直/振听/响应窗口/和了/流局/连庄（92.7%）
  agent/           13 个工厂名（random / shanten / defense / search × 打风）+ 超时回退（93.7%）
  agent/llm/       OpenAI 兼容 LLM Agent：top-k 候选重排 + 回退（93.2%）
  eval/            自对弈驱动、指标、ELO、报告（89.6%）
  store/           Store 接口 + Memory / MySQL / Redis 三实现 + 共用契约测试
  transport/       HTTP/WS、seq 续传、JWT、Room actor、指标（77.9%）
testdata/          金标牌谱与 fixture
```

## 当前状态（S1–S12 完成，2026-11-10 收口）

| 包 | 内容 | 覆盖 |
|---|---|---|
| `internal/tile` | 34 种牌 + 赤宝牌、确定性洗牌（SplitMix64 + Fisher-Yates）、牌山/王牌/岭上/宝牌指示 | **100%** |
| `internal/rules` | 判胡拆解 + 向听 + **41 役种/符/点数**（29 手金标）；v1 金标回归 39/39 | **96.2%** |
| `internal/game` | 副露/立直/振听/**显式响应窗口**（ron>pon/kan>chi）/和了/流局/连庄；整局脚本 + 回放逐字段一致 | **92.7%** |
| `internal/agent` | 四类策略（随机/向听/防守/搜索）× 三种打风 + 超时回退 + 工厂（**13 个名字**） | **93.7%** |
| `internal/agent/llm` | top-k 候选重排 + 解析/合法性校验 + 回退 + 统计（无 key 自动 Mock） | 93.2% |
| `internal/eval` | 自对弈驱动、和了/放铳/顺位/立直率、决策 P50/P95、95% CI、BT-ELO | 89.6% |
| `internal/store` | Store 接口 + Memory（89.7%）/ Redis（85.6%）/ MySQL（需 `MYSQL_TEST_URL`）同一套契约测试 | 见左 |
| `internal/transport` | HTTP/WS 事件流、`seq` 续传、JWT 座位、Room actor、响应窗口交互、指标 | **77.9%** |
| `cmd/riichi-mcp` | 5 个规则工具（shanten / ukeire / safety / score / yaku_potential） | 88.3% |

> 覆盖率口径：`go test ./... -cover`（`-cover` 是语句覆盖率）。
> `sql` 包在**没设** `MYSQL_TEST_URL` 时全部用例 skip，所以显示 0.0%——真连 MySQL 5.7.44 时约 80.7%（见契约 §5）。
> 本表数字与 `go test ./... -cover` 输出一致，改代码后请同步更新。

## 跑起来

四段命令，全部在 `riichi/` 下执行（Go 1.25+）。

### 1）跑一局：起服务 + 建局 + 打一张牌

```bash
cd riichi

# 终端 A：起服务（demo 用 dev 密钥；生产必须覆盖 RIICHI_JWT_SECRET）
go run ./cmd/riichi-server -addr 127.0.0.1:8099 -jwt-secret dev -cors "*"
# → riichi-server 启动 addr=127.0.0.1:8099 agent_timeout=500ms cors=*

# 终端 B：签一个座位 0 的 JWT（2h 有效）
go run ./cmd/riichi-server -jwt-secret dev -issue-token 0
# → eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9....（一整行）

TOKEN=<粘贴上面那行令牌>

# 建一局：seed 固定 → 牌局可复现；agents 第 0 位是 human，其余三家 AI
curl -s -X POST http://127.0.0.1:8099/api/v1/games \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"seed":42,"agents":["human","random","random","random"]}'
# → {"game_id":"...","seed":42,"seq":2,"agents":[...],"ruleset":{...},"created_at":"..."}

GAME=<上面的 game_id>

# 看权威状态（只给本座位手牌 + legal_actions）
curl -s "http://127.0.0.1:8099/api/v1/games/$GAME" -H "Authorization: Bearer $TOKEN"
# → {"game_id":"...","seq":2,"phase":"await_discard","turn":0,"wall_remaining":69,
#    "hand":["P","9p",...],"drawn":"9m","legal_actions":[{"kind":"discard","tile":"P"},...],...}

# 出牌没有 HTTP 接口，走 WS（见下方协议）；HTTP 侧只有「建局 / 查状态 / 全量事件 / 指标」

# 全量事件（回放）/ 单局指标 / 进程指标 / 健康检查
curl -s "http://127.0.0.1:8099/api/v1/games/$GAME/replay" -H "Authorization: Bearer $TOKEN"
curl -s "http://127.0.0.1:8099/api/v1/games/$GAME/metrics" -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:8099/metrics
curl -s http://127.0.0.1:8099/healthz          # → {"status":"ok"}
```

- WS：`/api/v1/ws/games/{id}?seat=0&token=<token>`，连上后**先发** `{"type":"resume","last_seq":0}` 拿历史事件，再发 `{"type":"action","seq":N,"action_id":"uuid","action":{...}}` 出牌。
- 事件流、错误码、续传语义见契约 §6/§7；实现说明见 `internal/transport/doc.go`。

### 2）跑评测：自对弈 + 指标 + ELO

```bash
cd riichi

# 验收口径：20 场半荘（seed 1-20），报告写到 .tmp-eval（已被 git exclude，不会误提交）
go run ./cmd/riichi-eval --count 20 --out .tmp-eval

# 默认：seed 1-100、四家 shanten:attack / defense:defense / search:closed / random
go run ./cmd/riichi-eval --seeds 1-100

# 换 agent 组合 / 调并发 / 只打一局 smoke
go run ./cmd/riichi-eval --seeds 1-50 --agents shanten:attack,shanten:attack,defense:defense,search:closed
go run ./cmd/riichi-eval --count 20 --jobs 8
go run ./cmd/riichi-eval --count 20 --round-limit 1

# 报告口径：允许鸣牌（--calls agent）；seed 7 已修，最新报告 441cc8c 为 20 场 / 218 轮 / 0 aborted
go run ./cmd/riichi-eval --count 20 --calls agent --out .tmp-eval

# 看产物
cat .tmp-eval/report.md          # 人读表格；report.json 机器读；manifest.json 记 git sha / 配置 hash
```

LLM 座位（需 OpenAI 兼容 key；没有 key 时用 `-llm-mock` 只验证链路）：

```bash
go run ./cmd/riichi-eval --count 10 --calls=agent --timeout 5s \
  --agents llm,shanten:attack,defense:defense,search:closed \
  --llm-base-url "$LLM_BASE_URL" --llm-model "$LLM_MODEL" --llm-api-key "$LLM_API_KEY" \
  --out .tmp/llm-real
# 无 key 验证链路：加 -llm-mock（走 MockClient，不联网）
```

预期（20 场口径的**历史记录摘录**，完整列见 `.tmp-eval/report.md`）：

```
座位   agent                决策数   和了率             放铳率             平均顺位          立直率   副露/轮
0      shanten:attack       2750   41.3% [33.9,48.6]  14.0% [8.8,19.1]  2.45 [1.97,2.93]  13.4%   1.27
1      defense:defense      2690   23.3% [16.9,29.6]  11.0% [6.4,15.7]  1.95 [1.49,2.41]  43.6%   0.0
2      search:closed        2648   21.5% [15.4,27.7]  12.2% [7.3,17.1]  1.90 [1.59,2.21]  40.1%   0.0
3      random               2420    0.0% [0.0,0.0]    29.1% [22.3,35.9]  3.70 [3.49,3.91]   0.0%   0.94
ELO：1639.5 / 1628.7 / 1523.0 / 1208.8（Bradley-Terry，均值 1500）
共 20 场 / 172 轮，失败 0 场；和了 148（自摸 34 / 荣和 114），立直 167，副露 380（2.2 次/轮），37.8% 的和了来自副露手
```

> 数字会随策略实现变化，以你自己跑出来的 `report.md` 为准。
> 策略层 2026-11-16 起：最优切牌有立直选项即立直；响应窗口按「鸣后向听是否改善」选择性鸣牌（门清/守備打风不鸣，有人立直不鸣）。
> 门清口径（`--calls pass`）20 场 / 151 轮 / 0 副露 / 立直 204 次（立直率 21.9%–60.9%）、ELO 1710.7/1558.0/1552.6/1178.7——**只作为对比，不能当 headline**，两版样本不可直接比较。

### 3）用 MCP 工具（stdio JSON-RPC）

```bash
cd riichi

# 列工具：一行一条 JSON-RPC 消息（注意不能只写 tools/list，那不是合法 JSON-RPC）
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | go run ./cmd/riichi-mcp

# 调用工具：向听
echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"mahjong.shanten","arguments":{"hand":["1m","2m","3m","4m","5m","6m","7m","8m","9m","1p","2p","3p","5p"]}}}' \
  | go run ./cmd/riichi-mcp

# 算点 / 役种潜力 / 危险度同理，换 name 与 arguments 即可
echo '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mahjong.score","arguments":{"hand":["1m","2m","3m","4m","5m","6m","7m","8m","9m","1p","2p","3p","5p"],"winning":"5p","ctx":{"tsumo":true,"riichi":true,"menzen":true}}}}' \
  | go run ./cmd/riichi-mcp
```

5 个工具：`mahjong.shanten`（向听）、`mahjong.ukeire`（有效进张）、`mahjong.safety`（危险度排序）、`mahjong.score`（役/翻/符/点）、`mahjong.yaku_potential`（听牌形可成立的役种）。
把它接进 Claude Desktop / Cursor / 自研 Agent 时，MCP 客户端发 `initialize` → `notifications/initialized` → `tools/list` → `tools/call`。
协议约束：参数错 `-32602`、内部错 `-32603`、解析失败 `-32700`；单条消息上限 1 MiB；纯标准库实现，无第三方依赖。

### 4）开网页：最小牌桌

```bash
cd riichi

# 终端 A：起服务（CORS 放行，方便 file:// 打开的页面直连）
go run ./cmd/riichi-server -addr 127.0.0.1:8099 -jwt-secret dev -cors "*"

# 终端 B：签座位 0 的令牌
go run ./cmd/riichi-server -jwt-secret dev -issue-token 0

# 浏览器打开 web/index.html（file:// 即可，纯静态、无构建）
#   服务器 = http://127.0.0.1:8099   座位 = 0   JWT = 终端 B 的令牌
#   点「新建对局」→ 自动建局 + 连接；点自己的手牌 = 打牌
#   其余三家是 AI，会自动摸打；断线按钮可验证 resume 补齐
```

后端关了 CORS（`-cors ""`）时用同源反代兜底：

```bash
python web/serve.py --port 8098 --target http://127.0.0.1:8099
# 浏览器打开 http://127.0.0.1:8098/（状态栏会显示「已连接（同源代理）」）
```

页面能力与手动验收步骤见 [`web/README.md`](../web/README.md)（含断线重连补发、刷新续上的逐条预期）。

观战：同一局可以开只读连接（无需 JWT、不接收 `responses`、提交动作回 `ILLEGAL_ACTION`），事件流与座位端逐条一致：

```bash
# ws://127.0.0.1:8099/api/v1/ws/games/{id}?spectator=1
# 也可以写 ?seat=spectator；握手后先发 {"type":"resume","last_seq":0} 补齐历史
```

多实例观战（跨实例镜像）：两个进程共用同一个 Redis（事件存储 + 广播频道），对局建在 A、观战端连 B：

```bash
export RIICHI_REDIS_URL=redis://127.0.0.1:6379/0
# 实例 A
go run ./cmd/riichi-server -addr :8099 -jwt-secret dev -redis-url "$RIICHI_REDIS_URL"
# 实例 B（另一个终端）
go run ./cmd/riichi-server -addr :8100 -jwt-secret dev -redis-url "$RIICHI_REDIS_URL"

# 观战端连 B 也能看 A 上的对局：
# ws://127.0.0.1:8100/api/v1/ws/games/{id}?spectator=1
# B 订阅 riichi:room:<id>，先从共享 store 回放历史，再转发实时帧（按 seq 去重）
```

> 当前跨实例只支持**只读观战**；座位动作（打牌/响应）必须发到房间 owner 实例。owner 注册表与动作转发是下一步。

### 5）部署 demo（Docker + Nginx）

`riichi/deploy/` 里是可直接用的部署件：

| 文件 | 作用 |
|---|---|
| `Dockerfile` | 多阶段构建（Go 1.25 → alpine），非 root 用户、healthcheck |
| `docker-compose.yml` | server + Redis（事件存储与跨实例广播都用 Redis） |
| `nginx.conf` | WebSocket 升级头、建局限流、`/metrics` 只对内网 |

```bash
export RIICHI_JWT_SECRET=$(openssl rand -hex 32)
docker compose -f riichi/deploy/docker-compose.yml up -d --build
curl http://127.0.0.1:8099/healthz
```

> 本机 Docker daemon 当前未启动，镜像构建未实跑；`go build` 与 compose 配置解析已校验。WS 压测（100 并发 p99）下一轮补。

## 设计要点

**事件溯源**：`状态 = fold(events)`，每个事件带单调 `seq`，牌山由 seed 决定。回放、断线续传、评测复现由同一机制解决；`Replay(seed, actions)` 与实时状态逐字段一致有测试。

**Observation 裁剪（防作弊）**：Agent 的唯一输入是 `game.Observe(seat)` 的结果，只含自己手牌 + 公开信息；别家手牌、牌山顺序、里宝牌指示牌**结构上不可见**，切片全部拷贝。契约要求「Observe 不泄漏」必须有测试。

**响应窗口优先级**：打牌/加杠后进入 `await_response`，四家并发选择，结算顺序 `ron > pon/kan > chi > pass`；双响按 `Ruleset.DoubleRon`（关闭时头跳取最近一家），pon/kan 取离放铳者最近的一家，chi 只来自下家；超时按 pass，放弃荣和记同巡振听（立直者永久振听）。

**为什么不用 `math/rand`**：牌谱复现要求「同 seed 在任何机器、任何 Go 版本上得到同一副牌」。标准库 `Shuffle` 的实现细节可能随版本变化，而 SplitMix64 只有加法与位运算，实现固定；洗牌用 Fisher-Yates + 拒绝采样取模，保证分布均匀。

**牌山结构**：

```
136 张
├── 活牌 122：开局发 4×13=52，剩 70 张按摸牌顺序消耗
└── 王牌 14
    ├── 岭上牌 4（杠后摸）
    ├── 宝牌指示牌 5（开局翻 1，每杠 +1）
    └── 裏宝牌指示牌 5
```

**Room actor**：一局一个 goroutine，所有改状态的操作（action / AI 结果 / resume / 响应超时）经 channel 串行执行，对局状态无锁；广播用带缓冲 channel，慢连接直接摘除，不阻塞对局。

**真实算点**：`game.RulesScorer` 把副露转成 `rules.Meld` → `Decompose` → `Evaluate` → 宝牌/赤/里 → `Score`，门清/副露/七对/国士都走真实规则。

**MCP 工具层**：`riichi-mcp` 只依赖纯函数层（rules/tile），无状态、无网络，可被任何 MCP 客户端接入；LLM Agent 则用同一批规则算 top-5 候选，只做重排与解释。

**编码**：`Kind` 0-33（万/筒/索 各 9 + 字牌 7），`Tile{Kind, Aka}`；文本 `"5m"`、赤宝牌 `"r5m"`（兼容 `"0m"`），字牌 `"E"`/`"1z"` 两种写法都可解析。

## 契约

接口与事件/协议定义见 [`docs/riichi-接口契约-20261110.md`](../docs/riichi-接口契约-20261110.md)（冻结 v1.3，改契约需 ADR）。
依赖方向铁律：`transport → game → rules → tile`，只能向下；`agent` 只读 `game`，禁止回写状态。


> 对局推进：`Config.AutoAdvanceRounds` 默认开启——`round_end` 后自动开下一局，一路打到南 4 / トビ終了并广播 `game_end`（含 `ranks` 终局顺位）；单局调试可显式传 `false`。

## 已知限制（诚实口径，别在面试里被拆穿）

1. **立直率 ≈ 0（实测四个座位都是 0.0%）**：S8 的策略只输出 `ActDiscard`，不选 `ActRiichi`，`random` 也一样。引擎侧立直/振听已实现，缺的是策略层动作选择。
2. **真 LLM 未接真 key**：`NewFromEnv()` 没有 `LLM_API_KEY`/`OPENAI_API_KEY` 时自动用 `MockClient`（决策链路/解析/回退/统计照常跑）；只有配了 key 才走 OpenAI 兼容端点。
3. **评测默认仍禁鸣牌，但副露已能真跑**：CLI 默认 `--calls pass`（只荣和）；S5b 起「鸣牌后打牌」已接通，`--calls agent` 可真实跑副露博弈（最新报告口径：218 轮 / 1757 次副露）。**seed 7 的死状态已修**（`d61a7fb`）：此前座位 0 有 4 组副露、手牌只剩 2 张，吃 5m 后食替禁则把两张手牌全禁掉 → `Legal()` 为空 → 该场 aborted；修法是开窗前先前瞻校验「这次副露后是否还有合法打牌」，禁满就不给该吃选项，并补了 `call_guard_test.go`。修复后重跑 20 场 / 218 轮：aborted=0 / errors=0 / 副露 1757 / 和了 192（自摸 46 / 荣和 146）。
4. **MySQL JSON 归一化**：JSON 列会 key 重排/去空白，回放按**语义**比较，不做字节级断言（契约 §5）。
5. **CI 覆盖来得晚**：`riichi` 已接入 CI——`.github/workflows/ci.yml` 的 `riichi-test` job 跑 `gofmt` + `go vet` + `go test ./... -race`，并挂 MySQL 5.7 service 真跑存储契约；但这是 S12 才补的，早期只有本地门禁。
6. **事件全量广播**：事件（含别人摸牌）按全量广播，尚未按座位过滤；`agent_thought` 是广播消息、**不落库**，重连后看不到历史思考。
7. **单进程内存房间**：`Manager` 在进程内存里管 room，重启丢房间（事件仍在 store 里）；多进程部署需要把 room 与会话外置。
8. **样本量**：20 场 / 218 轮，比例指标 95% CI 约 ±5~6 个百分点，区分不了上表 32.1% 与 28.4% 那点差距；要下更细结论需 ≥200 场。

## 开发

```bash
cd riichi
gofmt -l .          # 应为空
go vet ./...
go test ./... -count=1 -cover
go test ./internal/tile -run "^$" -bench . -benchmem     # 洗牌/建牌墙基准
go test ./internal/agent -run "^$" -bench . -benchmem    # 三策略 Decide 基准
```

Go 1.25+。评测输出目录（默认 `./eval-out`）不要提交，建议 `--out .tmp-eval` 或加进本地 exclude。
