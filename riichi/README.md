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

## 当前状态（M0）

- [x] `internal/tile`：34 种牌 + 赤宝牌编码/解析、确定性洗牌（SplitMix64 + Fisher-Yates）、牌山切分（活牌 122 / 王牌 14）、发牌、摸牌、岭上、宝牌指示牌推进
- [x] 单测覆盖 88%（目标 90%+，M1 补齐）
- [ ] rules / game / agent / eval / transport

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
go test ./internal/tile -run . -bench .  # 后续补 bench
```

Go 1.25+。
