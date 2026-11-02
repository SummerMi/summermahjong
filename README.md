# 广东麻将（推倒胡）后端服务

> 一个用 **FastAPI（Python）+ Go 双实现**的广东麻将规则后端服务。
> 目标不是做麻将游戏，而是用一件「有真实算法难度」的事情，把 **规则引擎 / 分层架构 / 三种存储 / 工程化 / 跨语言性能对比** 讲清楚。

![Swagger UI](docs/images/swagger-full.png)

**一句话数据**：Python 侧 **163 个测试全绿**（另有 11 个 MySQL 门控集成用例）、**三种存储实现**（内存 / SQL / Redis）行为一致、**真实 MySQL 5.7.44 实测**并修掉两个只在 MySQL 暴露的缺陷、Go 侧 **15 个测试函数 + 2000 手随机牌逐条对拍**、判胡 Go 比 Python 快 **45×–100×**（0 allocs/op）、单 worker HTTP **约 2500 RPS / 低负载 p99 5.6ms**（本机 loopback，非生产）、Docker 非 root + CI 全绿。

---

## 1. 为什么做这个项目

麻将规则天生适合当后端练习场：

- **有真算法**：胡牌判定（4 面子 + 1 将 / 七对）需要递归回溯，听牌要对 34 种牌做增量试算——足以白板手写、经得起追问；
- **有真状态**：一局牌 = 牌墙 + 四家手牌 + 四家牌河 + 当前玩家，天然涉及状态机、并发保护、生命周期（TTL / 上限）和持久化（重启后还能查）；
- **有真约束**：牌只有 136 张、34 种，每种最多 4 张——输入校验不是走过场，是业务的一部分；
- **能讲性能**：同一套算法用 Python 和 Go 各写一遍，用 benchmark 数据说话，而不是拍脑袋说「Go 快」；
- **能讲一致性**：同一份状态迁移纯函数，被内存、SQL、Redis 三种存储复用——「换实现不换行为」有测试兜底。

## 2. 功能

| 能力 | 说明 |
|---|---|
| 胡牌判定 | 14 张手牌 → 标准型（4 面子 + 1 将）或七对；13 张（未摸牌）直接判未胡 |
| 听牌计算 | 13 张手牌 → 全部能胡的牌种（升序），自动跳过已占满 4 张的牌种 |
| 牌墙 / 发牌 | 34 种 × 4 张 = 136 张；庄家 14 张、其余 13 张；支持 `seed` 复现同一牌局 |
| 对局管理 | 创建 / 查询 / 打牌；100 局上限 + 7200s 闲置清理（三种存储都生效） |
| **三种存储（可切换）** | 默认内存（零依赖）；`DATABASE_URL` → SQLAlchemy 2.0 异步（SQLite 验证 / **MySQL 5.7 实测**）；`REDIS_URL` → Redis（Hash + EXPIRE + WATCH + ZSET 做 LRU） |
| **并发正确性** | 打牌走事务 + `version` 乐观锁（MySQL 方言额外 `SELECT ... FOR UPDATE`；Redis 用 `WATCH/MULTI`），冲突返回 409 `GAME_CONFLICT` |
| **MySQL 死锁修复** | 隔离实例上实测出 1213 死锁，用「维护事务与插入事务分离 + 有限次退避重试」修到 0 逃逸 |
| **Go 对照实现** | `go-engine/` 纯标准库重写同一套规则，与 Python 逐条对拍 + benchmark 对比 |
| **压测工具** | `go-engine/cmd/loadgen`（Go 压测器）+ `scripts/bench_http.py`（Python 版），输出 RPS / p50 / p90 / p99 |
| 四则运算 | 保留最基础的计算器接口，用来演示统一错误信封与参数校验 |
| 工程化 | Pydantic v2 校验、统一错误码、依赖注入、pytest（163 用例）、ruff、Docker、GitHub Actions |

## 3. 5 分钟跑起来

```bash
# 1) 建虚拟环境并安装（Python 3.12+）
python -m venv .venv
# Windows: .venv\Scripts\activate    macOS/Linux: source .venv/bin/activate
pip install -e ".[dev]"

# 2) 起服务（默认内存模式）
python -m uvicorn app.main:app --reload --port 8000
```

打开 <http://127.0.0.1:8000/docs> 直接在线调用；根路径 `GET /` 返回服务简介。

> Windows 控制台默认 GBK，中文日志会显示成乱码，先执行 `set PYTHONUTF8=1`（PowerShell 用 `$env:PYTHONUTF8=1`）。

三种存储怎么切（优先级 `REDIS_URL` > `DATABASE_URL` > 内存）：

```bash
# SQLite：真实落库，重启进程后同一 game_id 仍可查
set DATABASE_URL=sqlite+aiosqlite:///./data/mahjong.db

# MySQL：需要先装驱动  pip install -e ".[mysql]"
set DATABASE_URL=mysql+aiomysql://user:pass@127.0.0.1:3306/mahjong

# Redis：需要先装客户端  pip install -e ".[redis]"（或 docker compose up -d redis）
set REDIS_URL=redis://127.0.0.1:6379/0

python -m uvicorn app.main:app --port 8000
```

Go 引擎与基准：

```bash
cd go-engine
go test ./... -count=1                     # 对拍 Python 生成的 2039 条用例
go test ./mahjong -bench . -benchmem -run '^$'   # 性能基准
go run ./cmd/bench                         # 打印每个用例的 ns/op、µs/op
```

## 4. 技术栈

| 层 | 选型 | 用途 |
|---|---|---|
| Web 框架 | FastAPI 0.115+ | 路由、依赖注入、自动生成 OpenAPI |
| 数据校验 | Pydantic v2 | 请求 / 响应模型，字段级校验与示例 |
| 配置 | pydantic-settings | 环境变量 / `.env` 覆盖 |
| 持久化 | SQLAlchemy 2.0（异步）+ aiosqlite / aiomysql | 内存 / SQL / Redis 三实现，SQLite 与 MySQL 5.7 都实测过 |
| 缓存/存储 | redis-py（asyncio） | Hash 存快照 + EXPIRE + WATCH/MULTI + ZSET 做 LRU |
| 服务端 | Uvicorn | ASGI 服务器 |
| 测试 | pytest + httpx + fakeredis | 163 个用例；MySQL 集成用例用 `MYSQL_TEST_URL` 门控 |
| 代码规范 | ruff | line-length 100 |
| 跨语言实现 | Go 1.25（纯标准库） | 规则引擎对照实现 + benchmark + 压测器 |
| 容器 | Docker（python:3.12-slim） | 非 root 运行 + HEALTHCHECK |
| CI | GitHub Actions | push/PR 跑 ruff + pytest |

## 5. 架构

```
                         ┌──────────────────────────────┐
   HTTP / JSON  ────────▶│  app/api/routes/*            │  路由层：取参数 → 调服务 → 组装响应
                         │  health / calc / mahjong / games
                         └──────────────┬───────────────┘
                                        │ Depends 注入（只依赖抽象 GameRepository）
                         ┌──────────────▼───────────────┐
                         │  app/services/repository/*   │  仓库层：统一接口 + 三种实现
                         │  base.py   抽象 + 共用纯函数  │
                         │  store.py  内存（asyncio.Lock）│
                         │  sql.py    SQL（乐观锁/行锁）  │
                         │  redis.py  Redis（WATCH/EXPIRE）│
                         └──────────────┬───────────────┘
                                        │
                         ┌──────────────▼───────────────┐
                         │  app/services/mahjong/*      │  领域层：纯函数规则引擎
                         │  tiles.py + rules.py         │  （Go 版在 go-engine/ 同构实现）
                         └──────────────┬───────────────┘
                                        │
                         ┌──────────────▼───────────────┐
                         │  app/core/*                  │  横切：配置、业务异常、统一错误信封
                         └──────────────────────────────┘
```

四条分层原则：

1. **schema 只管格式，业务校验进服务层**：Pydantic 负责「是不是字符串数组」，手牌张数 / 未知牌 / 同种超 4 张由规则引擎判断——避免同一规则两套答案。
2. **路由只依赖抽象**：`StoreDep = Annotated[GameRepository, Depends(get_store)]`，用内存还是 SQL/Redis 由 `lifespan` 按配置决定，路由代码一行不改。
3. **三种存储共用同一份状态迁移纯函数**（`new_game_state` / `apply_discard`），「同 seed 换存储不换行为」有测试钉住。
4. **错误只有一种出口**：业务异常自带 `code + HTTP 状态码`，统一渲染成 `{"error": {...}}`。

### 目录结构

```
app/
├── main.py                 # create_app() 工厂 + lifespan（按配置选存储）+ CORS + 异常处理器
├── core/                   # config.py（DATABASE_URL / REDIS_URL）/ errors.py（统一信封）
├── schemas/                # Pydantic 请求 / 响应模型（只做格式校验）
├── api/                    # deps.py（select_storage）/ router.py / routes/*.py
└── services/
    ├── repository/         # base.py（抽象+纯函数）/ store.py（内存）/ sql.py（SQL）/ redis.py（Redis）
    └── mahjong/            # tiles.py / rules.py（纯函数引擎）
go-engine/                  # Go 实现 + 对拍 + benchmark + HTTP 压测器
scripts/                    # bench_http.py（Python 版压测，用于对照客户端瓶颈）
tests/                      # 163 个用例：算法 74 + API 47 + 持久化/SQL/Redis 31 + MySQL 门控 11
docs/                       # 工程约定 / 调研 / 面经 / 简历话术 / 部署 / 大厂计划 / ADR / 每日任务
```

## 6. API

统一前缀 `/api/v1`，完整交互文档在 `/docs`。错误统一为：

```json
{"error": {"code": "DIVISION_BY_ZERO", "message": "除数不能为 0"}}
```

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 服务简介（名称 / 版本 / 文档入口） |
| GET | `/healthz` | 健康检查：状态、版本、运行秒数 |
| GET | `/api/v1/mahjong/tiles` | 全副牌信息：136 张、34 种、固定顺序清单 |
| POST | `/api/v1/mahjong/win-check` | 胡牌判定 |
| POST | `/api/v1/mahjong/tenpai` | 听牌计算 |
| POST | `/api/v1/games` | 创建对局（可传 `seed` 复现） |
| GET | `/api/v1/games/{game_id}` | 查询对局（只暴露庄家手牌） |
| POST | `/api/v1/games/{game_id}/discard` | 打牌（牌进牌河、轮转下家、下家摸牌） |
| POST | `/api/v1/calc/{op}` | 四则运算，`op ∈ add / subtract / multiply / divide` |

### curl 示例

```bash
# 一把标准胡：123m 456m 789m 111p 东东
curl -X POST http://127.0.0.1:8000/api/v1/mahjong/win-check \
  -H 'Content-Type: application/json' \
  -d '{"hand":["1m","2m","3m","4m","5m","6m","7m","8m","9m","1p","1p","1p","E","E"]}'
# => {"is_win":true,"pattern":"standard"}

# 听牌：123456789m 111p 2s，听 2s
curl -X POST http://127.0.0.1:8000/api/v1/mahjong/tenpai \
  -H 'Content-Type: application/json' \
  -d '{"hand":["1m","2m","3m","4m","5m","6m","7m","8m","9m","1p","1p","1p","2s"]}'
# => {"waiting_tiles":["2s"]}

# 相同 seed 创建两次，拿到的牌局完全一致
curl -X POST http://127.0.0.1:8000/api/v1/games \
  -H 'Content-Type: application/json' -d '{"seed":42}'
# => {"game_id":"...","wall_remaining":83,"current_player":"east","hands":{...}}
```

### 错误码

| HTTP | code | 场景 |
|---|---|---|
| 400 | `HAND_INVALID` | 手牌张数不对 / 未知牌 / 同种牌超过 4 张 |
| 400 | `TILE_NOT_IN_HAND` | 要打出的牌不在该座位手牌里 |
| 400 | `DIVISION_BY_ZERO` | 除数为 0 |
| 400 | `NOT_YOUR_TURN` | 还没轮到该座位出牌 |
| 404 | `GAME_NOT_FOUND` | 对局不存在（或已被 TTL 清理 / 上限淘汰） |
| 409 | `GAME_CONFLICT` | SQL / Redis 模式下并发写同一局，乐观锁冲突 |
| 422 | —（FastAPI 默认） | 请求体格式错误，返回字段级 `loc / msg` |
| 500 | `INTERNAL_ERROR` | 兜底异常，对外不泄漏内部细节 |

## 7. 胡牌算法（重点）

牌面编码：`1m~9m`（万）、`1p~9p`（筒）、`1s~9s`（条）、`E S W N P F C`（东南西北白板发财红中），34 种 × 4 张 = 136 张。

```text
把 14 张手牌映射成 34 长度的计数数组（下标 = TILE_KINDS 顺序）

标准型：for 每种计数 ≥ 2 的牌作为「将牌」:
            扣掉将牌 → 对剩余 12 张调用 can_form_melds(counts)
        can_form_melds：从最小的非零牌开始
            if 计数 ≥ 3: 扣 3 张（刻子）→ 递归
            if 是序数牌且下标 ≤ 6 且连续三张都 ≥ 1: 扣顺子 → 递归
            都不可行则回溯
七对：  所有牌种计数均为偶数，且总对数 == 7（4 张相同算两对）
听牌：  对 34 种牌逐一「试加一张」再判定，跳过手牌中已有 4 张的牌种
```

- **为什么用 34 定长数组而不是 `dict`**：牌种固定、下标即顺序，递归时「从最小非零位扫描」天然不重不漏，且没有哈希开销；
- **为什么先枚举将牌**：将牌必然来自计数 ≥ 2 的牌（最多 5 种），扣掉后问题降级成「纯面子拆分」，分支数骤减；
- **性能**：Python 单次判胡约 **5.7µs**、九面听听牌约 **164µs**；Go 版相同用例 **86ns / 3.5µs**（见下节）。纯 CPU、无 I/O，所以路由里同步调用，不套 `run_in_threadpool`。

## 8. Go 对照实现与性能对比

`go-engine/` 是同一套规则的 Go 重写（纯标准库，无第三方依赖），**不是玩具**：

- **语义等价有证据**：39 条精选用例（九面听 / 空听 / 七对 / 11 条非法输入）+ **2000 手随机牌**的期望值由 Python 引擎真实生成（fixture 里带 Python 版本与源码 sha256），Go 侧逐条对拍一致；
- **唯一有意差异**：洗牌 PRNG（Go `math/rand` vs Python `random.Random`），跨语言同 seed 牌墙不同，各自语言内可复现——README 里写明了取舍；
- **0 分配**：判胡路径 `0 B/op、0 allocs/op`（计数数组在栈上、不逃逸），可以现场 `go build -gcflags='-m'` 证明。

同机、单线程、**非压测**（Ryzen 7 7745HX / Go 1.25.6 / Python 3.12.3，输入同一份 `bench_cases.json`）：

| 用例 | Python | Go | 倍数 |
|---|---|---|---|
| 标准胡牌 | 5.71 µs | 86 ns | ~66× |
| 需要回溯的胡牌 | 4.52 µs | 93 ns | ~49× |
| 七对胡牌 | 12.0 µs | 212 ns | ~57× |
| 字牌刻子胡 | 6.25 µs | 76 ns | ~82× |
| 不成胡（散牌） | 3.22 µs | 32 ns | ~100× |
| 听牌（两面） | 122 µs | 2.14 µs | ~57× |
| 九面听 | 164 µs | 3.54 µs | ~54× |
| 七对听 | 262 µs | 5.52 µs | ~47× |

> 口径说明：数字来自 `timeit` / `go test -bench` 的本机单次循环，**不是压测，不报 QPS**；同一机器不同输入浮动约 5%。

## 9. 测试、压测与真实环境验证

```bash
python -m pytest          # 163 passed, 11 skipped（MySQL 门控用例默认跳过）
python -m ruff check app tests
cd go-engine && go vet ./... && gofmt -l . && go test ./... -count=1
```

| 范围 | 用例数 | 覆盖内容 |
|---|---|---|
| `tests/test_mahjong_tiles.py` | 33 | 牌墙 136 张 / 34 种各 4 张、seed 可复现、发牌张数、庄家 14 张、排序、计数、非法输入 |
| `tests/test_mahjong_rules.py` | 41 | 标准型各种拆法、七对、字牌不能组顺子、跨花色不成顺、九面听、死听、13/14 张边界 |
| `tests/test_api_*.py` | 47 | 全部端点成功路径 + 400/404/422 错误信封、seed 复现、上限淘汰、TTL 清理、CORS 预检 |
| `tests/test_persistence.py` | 18 | 内存/SQL 双实现、跨实例读取、重启后可查、乐观锁并发、DSN 掩码 |
| `tests/test_sql_repository.py` | 11 | 重试策略只重试 1213/1205、退避上限与抖动、best_effort 降级、亚毫秒时间精度、TTL 边界、20 并发建局 |
| `tests/test_redis_repository.py` | 13 | fakeredis 下的 create/get/discard/clear、TTL、LRU、15 路并发、三实现一致性 |
| `tests/test_mysql_integration.py` | 11（门控） | 真实 MySQL：建表、全流程、重启可查、CAS 409、同 seed 一致性、EXPLAIN 前置数据 |
| `go-engine/mahjong/*_test.go` | 15 函数 / 54 run | 39 条精选 fixture + 2000 条随机对拍 + 正负样本自检 |

CI：`.github/workflows/ci.yml` 在每次 push / PR 上跑 ruff + pytest（Python 3.12）。

### 9.1 真实 MySQL 5.7 实测（本仓库最有价值的一段工程记录）

在隔离实例（MySQL 5.7.44，端口 3307，不碰本机 3306）上跑了整套持久化，**暴露出两个只在 MySQL 出现、SQLite 全绿时看不到的缺陷**：

| 缺陷 | 现象 | 根因 | 修法 | 复测 |
|---|---|---|---|---|
| **Float 精度** | TTL/LRU 用例概率性失败（12 次里 8 次） | `Float` 映射成 MySQL 4 字节 `FLOAT`，epoch 秒（1.79e9）ULP≈128s，文本协议读回偏差可达数百秒 | 列类型改 `Double`（8 字节） | TTL/LRU **12/12 轮全过**，最大读回误差 0.000000 ms |
| **并发建局 1213 死锁** | 20 协程同时建局时偶发死锁 | `create()` 事务内 `DELETE ... WHERE last_access < ?` 的 next-key 锁与并发 `INSERT` 互斥 | 维护事务（清理/淘汰）与插入事务拆开；对 1213/1205 做**有限**退避重试（3 次、指数退避 + 抖动） | 20 协程 × 10 轮 = **200 次建局 0 逃逸**；再用行锁硬造 1205 验证重试路径有效 |

完整的 `SHOW ENGINE INNODB STATUS` 死锁原文、`EXPLAIN` 输出（主键 `type=const`、CAS `type=range`、LRU 走覆盖索引）、原始复测日志都在 [docs/mysql-实测报告.md](docs/mysql-实测报告.md)。

> 老库迁移注意：`create_all` 不会修改已存在表的列类型，从旧版本升级需手工执行
> `ALTER TABLE games MODIFY last_access DOUBLE NOT NULL;`（或引入 Alembic）。

### 9.2 HTTP 压测（本机 loopback，Go 客户端）

```bash
python -m uvicorn app.main:app --port 8000          # 另开一个终端
cd go-engine
go run ./cmd/loadgen -base http://127.0.0.1:8000 -path /healthz -c 100 -d 10s
go run ./cmd/loadgen -base http://127.0.0.1:8000 \
  -path /api/v1/mahjong/tenpai -method POST \
  -body '{"hand":["1m","2m","3m","4m","5m","6m","7m","8m","9m","1p","1p","1p","2s"]}' -c 100 -d 10s
```

实测（单 worker、纯内存模式、Ryzen 7 7745HX、客户端与服务端同机）：

| 场景 | 并发 | RPS | p50 | p99 |
|---|---|---|---|---|
| `GET /healthz` | 100 | **2888** | 32.2 ms | 73.7 ms |
| `POST /mahjong/tenpai`（最重，34 次判胡） | 100 | **2511** | 38.9 ms | 50.6 ms |
| `POST /mahjong/tenpai` | 10 | **2585** | 3.77 ms | 5.59 ms |
| `POST /mahjong/win-check` | 50 | **3758** | 13.0 ms | 17.3 ms |

怎么读这组数字：

- 并发从 10 提到 100，`tenpai` 的 RPS 几乎不变（2585 → 2511），而 p50 从 3.8ms 涨到 39ms —— 说明**单 worker 已经打满，多出来的并发只是在排队**；
- `win-check` 比 `tenpai` 快约 1.5 倍，符合「1 次判胡 vs 34 次判胡」的算法预期；
- **口径**：同机 loopback、无 TLS、无反向代理，客户端和服务端抢同一颗 CPU，**不是生产 QPS**；生产要多 worker + 独立压测机（wrk / hey）复测；
- 内存 store **不能** `--workers N`（每个进程一份字典 → 状态分裂），要横向扩必须切 SQL/Redis——这是设计约束，不是性能结论。

`scripts/bench_http.py` 是同一套压测的 Python 版，故意保留来对比：**客户端在 20 并发左右就自己先饱和**（实测 280 RPS、p50 36ms），正好说明「压测器选型也会影响结论」。

## 10. Docker

```bash
docker build -t gd-mahjong-api .
docker run --rm -p 8000:8000 gd-mahjong-api
# 或（compose 里带一个 redis:7-alpine 服务）
docker compose up --build
```

镜像基于 python:3.12-slim，要点：

- 先 `COPY pyproject.toml` 再装依赖，**只改源码时不重复下载依赖**（层缓存）；
- 以 `appuser`（uid 10001）**非 root** 运行；
- `HEALTHCHECK` 用标准库 `urllib` 打 `/healthz`，镜像里不需要 curl；
- 需要 MySQL / Redis 时，驱动在可选 extra 里（`[mysql]` / `[redis]`），构建时按需安装并传环境变量；只跑 demo 时镜像是纯内存、零外部依赖。

## 11. 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `APP_NAME` | 广东麻将（推倒胡）后端服务 | 服务名称 |
| `VERSION` | `0.1.0` | 版本号 |
| `CORS_ORIGINS` | `*` | 允许来源，逗号分隔；**生产必须收紧为具体域名** |
| `MAX_GAMES` | `100` | 对局数上限，满了淘汰最久未访问的（三种存储生效） |
| `GAME_TTL_SECONDS` | `7200` | 对局闲置多久后清理（三种存储生效） |
| `REDIS_URL` | 空 | 非空 → Redis 存储（优先级最高），如 `redis://127.0.0.1:6379/0` |
| `DATABASE_URL` | 空 | 非空 → SQL 存储，如 `sqlite+aiosqlite:///./data/mahjong.db` 或 `mysql+aiomysql://...` |
| `DB_ECHO` | `false` | 是否打印 SQL（调试用） |

三者优先级：`REDIS_URL` > `DATABASE_URL` > 内存；启动日志会打印实际选中的实现（DSN 密码已掩码）。

完整部署（本机 / Docker / 云服务器 / 免费平台）见 [docs/部署指南.md](docs/部署指南.md)。

## 12. 几个刻意的设计取舍

| 取舍 | 原因 | 生产怎么改 |
|---|---|---|
| 三种存储、**默认内存** | demo 零依赖启动；同时保留真实的数据库与缓存能力 | 生产配 `DATABASE_URL` / `REDIS_URL` 即切，路由不改 |
| 状态迁移抽成共用纯函数 | 三种存储的行为必须一致，否则「换存储换行为」 | 新增实现时复用同一组纯函数 |
| 乐观锁（`version` CAS）+ MySQL 行锁 + Redis `WATCH` | 低冲突场景不必付出悲观锁的串行化代价 | 高冲突改 `FOR UPDATE` / 队列串行化 |
| 维护事务与插入事务分离 | 同一事务里「先删再插」在 RR 下会形成间隙锁环（实测 1213） | 清理改后台任务，进一步解耦 |
| 有限重试（3 次、指数退避 + 抖动） | 只对「InnoDB 已回滚、重试安全」的 1213/1205 生效 | 接 Prometheus 告警，耗尽时降级 503 + Retry-After |
| JSON 列存手牌 / 牌河 | 对局是聚合根，一次读写整局；拆表会 N+1 | 分析需求走旁路事件表（CQRS 读模型） |
| 引擎同步调用 | 微秒级纯 CPU，套线程池反而多一次切换 | CPU 密集时拆独立 worker / 多进程 |
| 业务错误用 400 而非 500 | 422 给「格式错」，400 给「业务规则不满足」 | 按 code 埋点统计错误分布 |

## 13. Roadmap

- [x] 对局持久化（SQLAlchemy 异步 + 乐观锁）
- [x] Redis 存储实现（Hash + EXPIRE + WATCH + ZSET LRU）
- [x] 真实 MySQL 5.7 实测 + 两个缺陷修复（Float 精度 / 1213 死锁）
- [x] Go 引擎对照实现 + benchmark
- [x] 压测工具与报告（Go 压测器 + Python 版对照）
- [ ] cache-aside 缓存层（读缓存 + 写库删缓存）
- [ ] 鉴权（JWT）与「只能看自己手牌」
- [ ] WebSocket 多人实时对局
- [ ] 番型计算与出牌合法性完整化（碰 / 杠 / 吃）
- [ ] 前端牌桌 demo（可选）

## 14. 文档索引

| 文档 | 内容 |
|---|---|
| [docs/大厂冲刺计划-20261102.md](docs/大厂冲刺计划-20261102.md) | **2028 届时间轴、真实 JD/薪资/流程、16 周作战计划、投递日历** |
| [docs/面经高频题-20261102.md](docs/面经高频题-20261102.md) | 字节/腾讯/美团面经里的高频手撕题与八股清单（含打勾表） |
| [docs/W1每日任务表-20261102.md](docs/W1每日任务表-20261102.md) | 第一周 7 天逐日任务（题号 + 八股 + 项目 + 复盘） |
| [docs/目标公司投递清单-20261102.md](docs/目标公司投递清单-20261102.md) | P0/P1/P2 分层、入口、语言栈、投递节奏 |
| [docs/简历与面试话术.md](docs/简历与面试话术.md) | 简历项目条目、18 题面试 Q&A、9 步演示脚本、诚实边界 |
| [docs/mysql-实测报告.md](docs/mysql-实测报告.md) | MySQL 5.7 实测：EXPLAIN、死锁原文、两个缺陷的修复与复测 |
| [docs/adr-0002-持久化选型.md](docs/adr-0002-持久化选型.md) | 双实现、SQLite→MySQL、JSON vs 拆表、乐观锁 vs 悲观锁 |
| [docs/adr-0003-redis存储与缓存.md](docs/adr-0003-redis存储与缓存.md) | Redis 存储设计、WATCH 乐观锁、LRU、fakeredis 边界、cache-aside 演进 |
| [docs/市场调研-广深实习-20261101.md](docs/市场调研-广深实习-20261101.md) | 广深中小厂实习要求、薪资、与本项目的差距矩阵 |
| [docs/部署指南.md](docs/部署指南.md) | 本机 / Docker / 云服务器 / 免费平台部署与排错 |
| [docs/engineering-contract.md](docs/engineering-contract.md) | 工程约定：目录结构、牌编码、API 契约、代码规范、验收标准 |

## 15. 关于

一个面向**大厂后端实习**（Python / Go 方向）的实战项目：把「会写接口」升级成「能讲清算法、错误、并发、三种存储的一致性和跨语言性能」。

如果这个项目对你有帮助，欢迎提 issue 交流。
