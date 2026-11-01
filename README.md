# 广东麻将（推倒胡）后端服务

> 一个用 **FastAPI** 实现广东麻将规则的后端示范项目。
> 目标不是做一个麻将游戏，而是用一件「有真实算法难度」的事情，把 **规则引擎 / 分层架构 / 工程化** 三件事讲清楚：
> 胡牌怎么算、错误怎么统一、随机怎么可复现、服务怎么容器化上线、代码怎么测。

![Swagger UI](docs/images/swagger-full.png)

---

## 1. 为什么做这个项目

麻将规则天生适合当后端练习场：

- **有真算法**：胡牌判定（4 面子 + 1 将 / 七对）需要递归回溯，听牌需要对 34 种牌做增量试算——足以白板手写、经得起追问；
- **有真状态**：一局牌 = 牌墙 + 四家手牌 + 四家牌河 + 当前玩家，天然涉及状态机、并发保护和生命周期（TTL / 上限）；
- **有真约束**：牌只有 136 张、34 种，每种最多 4 张——输入校验不是走过场，是业务的一部分。

再加上广东麻将（推倒胡）本身与广州/深圳的地域关联，就成了一块很适合展示 Python 后端能力的题材。

## 2. 功能

| 能力 | 说明 |
|---|---|
| 胡牌判定 | 14 张手牌 → 标准型（4 面子 + 1 将）或七对；13 张（未摸牌）直接判未胡 |
| 听牌计算 | 13 张手牌 → 全部能胡的牌种（升序），自动跳过已占满 4 张的牌种 |
| 牌墙 / 发牌 | 34 种 × 4 张 = 136 张；庄家 14 张、其余 13 张；支持 `seed` 复现同一牌局 |
| 对局管理 | 创建 / 查询 / 打牌；内存仓库带 `asyncio.Lock`、100 局上限、7200s 闲置清理 |
| 四则运算 | 保留最基础的计算器接口，用来演示统一错误信封与参数校验 |
| 工程化 | Pydantic v2 校验、统一错误码、依赖注入、pytest、ruff、Docker、GitHub Actions |

## 3. 5 分钟跑起来

```bash
# 1) 建虚拟环境并安装（Python 3.12+）
python -m venv .venv
# Windows: .venv\Scripts\activate    macOS/Linux: source .venv/bin/activate
pip install -e ".[dev]"

# 2) 起服务
python -m uvicorn app.main:app --reload --port 8000
```

打开 <http://127.0.0.1:8000/docs> 直接在线调用；根路径 `GET /` 返回服务简介。

> Windows 控制台默认 GBK，中文日志会显示成乱码，先执行 `set PYTHONUTF8=1`（PowerShell 用 `$env:PYTHONUTF8=1`）。

用 uv 也可以：

```bash
uv venv && uv pip install -e ".[dev]"
uv run uvicorn app.main:app --port 8000
```

## 4. 技术栈

| 层 | 选型 | 用途 |
|---|---|---|
| Web 框架 | FastAPI 0.115+ | 路由、依赖注入、自动生成 OpenAPI |
| 数据校验 | Pydantic v2 | 请求 / 响应模型，字段级校验与示例 |
| 配置 | pydantic-settings | 环境变量 / `.env` 覆盖 |
| 服务端 | Uvicorn | ASGI 服务器 |
| 测试 | pytest + httpx | 121 个用例，覆盖算法与全部端点 |
| 代码规范 | ruff | line-length 100 |
| 容器 | Docker（python:3.12-slim） | 非 root 运行 + HEALTHCHECK |
| CI | GitHub Actions | push/PR 跑 ruff + pytest |

## 5. 架构

```
                         ┌──────────────────────────────┐
   HTTP / JSON  ────────▶│  app/api/routes/*            │  路由层：只做「取参数 → 调服务 → 组装响应」
                         │  health / calc / mahjong / games
                         └──────────────┬───────────────┘
                                        │ Depends 注入
                         ┌──────────────▼───────────────┐
                         │  app/services/*              │  服务层：状态迁移与业务校验
                         │  store.py（内存对局仓库）      │  asyncio.Lock / 上限 / TTL
                         │  mahjong/*（纯函数规则引擎）   │  tiles.py + rules.py
                         └──────────────┬───────────────┘
                                        │
                         ┌──────────────▼───────────────┐
                         │  app/core/*                  │  横切：配置、业务异常、统一错误信封
                         └──────────────────────────────┘
```

三条分层原则：

1. **schema 只管格式，业务校验进服务层**：Pydantic 负责「是不是字符串数组」，手牌张数 / 未知牌 / 同种超 4 张由规则引擎判断——避免同一规则在两处出现两套答案。
2. **路由不碰状态细节**：路由只拿 `GameState` 拼响应，把「能不能打、打完怎么轮转」留给 `GameStore`；以后换成 Redis 实现时路由不用改。
3. **错误只有一种出口**：业务异常自带 `code + HTTP 状态码`，由全局处理器统一渲染成 `{"error": {...}}`。

### 目录结构

```
app/
├── main.py                 # create_app() 工厂 + lifespan + CORS + 全局异常处理器
├── core/
│   ├── config.py           # Settings（环境变量 / .env）
│   └── errors.py           # AppError 体系 + 统一错误信封 + 500 兜底
├── schemas/                # Pydantic 请求 / 响应模型（只做格式校验）
├── api/
│   ├── deps.py             # SettingsDep / StoreDep
│   ├── router.py           # /api/v1 汇总
│   └── routes/             # health / calc / mahjong / games
└── services/
    ├── store.py            # 内存对局仓库（asyncio.Lock + 上限 + TTL）
    └── mahjong/
        ├── tiles.py        # 牌定义、牌墙、洗牌、发牌、排序、计数、校验
        └── rules.py        # 胡牌判定、听牌计算
tests/                      # 121 个用例：算法 74 + API 47
docs/                       # 工程约定 / 市场调研 / 简历话术 / 部署指南
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
- **性能**：单次 `winning_pattern` 约 6µs；最坏的九面听（九莲宝灯型）`waiting_tiles` 约 171µs；普通 13 张听牌约 55µs（本机 `timeit` 实测，**不是压测**，所以不报 QPS）。纯 CPU、无 I/O，所以路由里同步调用，不套 `run_in_threadpool`。

## 8. 测试与质量

```bash
python -m pytest          # 121 passed
python -m ruff check app tests
```

| 范围 | 用例数 | 覆盖内容 |
|---|---|---|
| `tests/test_mahjong_tiles.py` | 33 | 牌墙 136 张 / 34 种各 4 张、seed 可复现、发牌张数、庄家 14 张、排序、计数、非法输入异常 |
| `tests/test_mahjong_rules.py` | 41 | 标准型各种拆法、七对、字牌不能组顺子、跨花色不成顺、九面听、死听、13/14 张边界 |
| `tests/test_api_*.py` | 47 | 全部端点成功路径 + 400/404/422 错误信封、seed 复现、`max_games` 淘汰、TTL 清理、CORS 预检、配置覆盖 |

CI：`.github/workflows/ci.yml` 在每次 push / PR 上跑 ruff + pytest（Python 3.12）。

> 想自己验证上面这些数字：`python -m pytest`、`python -m ruff check app tests`，两条命令即可复现。

## 9. Docker

```bash
docker build -t gd-mahjong-api .
docker run --rm -p 8000:8000 gd-mahjong-api
# 或
docker compose up --build
```

镜像约 161MB（python:3.12-slim），要点：

- 先 `COPY pyproject.toml` 再装依赖，**只改源码时不重复下载依赖**（层缓存）；
- 以 `appuser`（uid 10001）**非 root** 运行；
- `HEALTHCHECK` 用标准库 `urllib` 打 `/healthz`，镜像里不需要 curl。

## 10. 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `APP_NAME` | 广东麻将（推倒胡）后端服务 | 服务名称 |
| `VERSION` | `0.1.0` | 版本号 |
| `CORS_ORIGINS` | `*` | 允许来源，逗号分隔；**生产必须收紧为具体域名** |
| `MAX_GAMES` | `100` | 内存中对局数上限，满了淘汰最久未访问的 |
| `GAME_TTL_SECONDS` | `7200` | 对局闲置多久后清理 |

完整部署（本机 / Docker / 云服务器 / 免费平台）见 [docs/部署指南.md](docs/部署指南.md)。

## 11. 几个刻意的设计取舍

| 取舍 | 原因 | 生产怎么改 |
|---|---|---|
| 内存 `GameStore` 而不是数据库 | 演示项目零依赖、启动即用；接口设计成「仓库」形态 | 换 Redis（分布式锁 + TTL）或 MySQL（对局表 + 状态机），路由不改 |
| `asyncio.Lock` 保护状态 | 单进程部署下够用，读-改-写不会交错 | 多 worker / 多实例时锁不跨进程，必须换 Redis 锁或数据库乐观锁 |
| 引擎同步调用 | 微秒级纯 CPU，套线程池反而多一次切换开销 | CPU 密集时拆独立 worker / 多进程 |
| 业务错误用 400 而非 500 | 422 给「格式错」，400 给「业务规则不满足」，两者语义不同 | 加埋点按 code 统计错误分布 |
| `seed` 可复现 | 让「随机牌局」可测试、可回放、可复现 bug | 回放系统 / 对局录像 |
| CORS 默认 `*` | demo 方便，任何前端都能调 | 收紧为具体域名，且不能与 `allow_credentials` 同时开 |

## 12. Roadmap

- [ ] 对局持久化（MySQL + SQLAlchemy，或 Redis）
- [ ] 简单鉴权（JWT）与「只能看自己手牌」
- [ ] WebSocket 实现多人实时对局
- [ ] 出牌合法性完整化（碰 / 杠 / 吃 / 胡）与番型计算
- [ ] 压测（locust）与性能报告
- [ ] 前端牌桌 demo（可选）

## 13. 文档索引

| 文档 | 内容 |
|---|---|
| [docs/engineering-contract.md](docs/engineering-contract.md) | 工程约定：目录结构、牌编码、API 契约、代码规范、验收标准 |
| [docs/市场调研-广深实习-20261101.md](docs/市场调研-广深实习-20261101.md) | 广州 / 深圳实习岗位要求调研、薪资、与本项目的差距 |
| [docs/简历与面试话术.md](docs/简历与面试话术.md) | 简历项目条目、12+ 道面试 Q&A、3 分钟演示脚本 |
| [docs/部署指南.md](docs/部署指南.md) | 本机 / Docker / 云服务器 / 免费平台部署与排错 |

## 14. 关于

一个面向广州 / 深圳 Python 后端实习岗位的实战项目：把「会写接口」升级成「能讲清规则、错误、并发和部署」。

如果这个项目对你有帮助，欢迎提 issue 交流。
