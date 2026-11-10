# riichi — 日麻多智能体服务（v2）

> 单人对三个 AI 打日麻（天凤式）；同时是「Go 后端 + Agent」的作品集项目。
> 设计文档见 [`docs/riichi-架构与里程碑-20261110.md`](../docs/riichi-架构与里程碑-20261110.md)。

## 目标

1. **规则引擎**：Go 实现日麻完整规则（136 牌 + 王牌/宝牌、吃碰杠、立直/振听、30+ 役种、符与点数、连庄/流局），事件溯源 + seed 可复现。
2. **对局服务**：HTTP + WS 事件流（`seq`/`last_seq` 断线续传）、JWT 座位绑定、事件表 + 快照 + 回放。
3. **多智能体**：统一 `Decide(Observation) → Action`，接入随机/向听/防守/搜索四类策略，三种打风（速攻/門前/守備）。
4. **评测**：固定 seed 自对弈，输出和牌率/放铳率/平均顺位/决策 P95 与 ELO。
5. **LLM + MCP**：规则工具（向听/有效牌/危险度/点数）以 MCP server 暴露，LLM 在 top-k 候选上重排并解释。

## 目录

```
cmd/
  riichi-server/   HTTP + WS 对局服务（待实现）
  riichi-mcp/      MCP server，暴露规则工具（待实现）
  riichi-eval/     自对弈评测 CLI（待实现）
internal/
  tile/            牌、牌山、赤宝牌、王牌（M0 完成）
  rules/           役种、符、点数、振听、立直（M1-M2）
  game/            对局状态机（M1）
  agent/           Agent 接口与四类策略（M4），llm/ 子包（M6）
  eval/            自对弈与指标（M5）
  store/           内存 / Redis / MySQL（M3）
  transport/       WS/HTTP 协议（M3）
  replay/          事件日志与快照（M3）
testdata/          金标牌谱与 fixture
```

## 当前状态（S1–S3、S6、S7 完成）

| 包 | 内容 | 覆盖 |
|---|---|---|
| `internal/tile` | 34 种牌 + 赤宝牌、确定性洗牌（SplitMix64 + Fisher-Yates）、牌山/王牌/岭上/宝牌指示 | **100%** |
| `internal/rules` | 判胡拆解 + 向听 + **41 役种/符/点数**（29 手金标）；v1 金标回归 39/39 | **94.5%** |
| `internal/game` | 对局流程：副露/立直/振听/响应窗口/和了/流局/连庄（71 用例，整局脚本+回放一致） | **92.2%** |
| `internal/agent` | 四类策略（随机/向听/防守/搜索）× 三种打风 + 超时回退 + 工厂（13 个名字） | **93.7%** |
| `internal/store` | Store 接口 + Memory 实现 + 契约套件；sql（MySQL）/redis 同一套契约测试 | memory 89.7% / sql 80.7%* / redis 85.6% |
| `internal/transport` + `cmd/riichi-server` | HTTP/WS 事件流、seq 断线续传、JWT 座位、Room actor、200 并发建局 | **75.9%** |

\* sql 需要 `MYSQL_TEST_URL`，未设置时 9 个用例 skip（默认零依赖）。

## 跑起来

`ash
cd riichi
go test ./... -count=1 -cover          # 全绿
go run ./cmd/riichi-server -addr 127.0.0.1:8099 -jwt-secret dev -issue-token 0
# 另开终端：
curl -s -X POST localhost:8099/api/v1/games -H "Authorization: Bearer <token>" \
  -d '{"seed":42,"agents":["human","random","random","random"]}'
`

- WS：`/api/v1/ws/games/{id}?seat=0&token=<token>`，连上后先发 `{"type":"resume","last_seq":0}` 拿历史事件。
- 事件流、错误码、续传语义见 `docs/riichi-接口契约-20261110.md` §6/§7；实现说明见 `internal/transport/doc.go`。

## 契约
接口与事件/协议定义见 [`docs/riichi-接口契约-20261110.md`](../docs/riichi-接口契约-20261110.md)（冻结 v1，改契约需 ADR）。

## 设计要点

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

**编码**：`Kind` 0-33（万/筒/索 各 9 + 字牌 7），`Tile{Kind, Red}`；文本 `"5m"`、赤宝牌 `"r5m"`（兼容 `"0m"`），字牌 `"E"`/`"1z"` 两种写法都可解析。

## 开发

```bash
cd riichi
gofmt -l .          # 应为空
go vet ./...
go test ./... -count=1 -cover
go test ./internal/tile -run "^$" -bench . -benchmem
```

Go 1.25+。
