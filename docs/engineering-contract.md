# 工程约定 v1（Lead 制定，两位工程 teammate 共同遵守）

> 目标：把本仓库从「学习脚本 + 玩具路由」改造成**能投实习、能当场演示、能被面试官追问**的 Python 后端项目。
> 主题：广东麻将（推倒胡）后端服务。选这个主题的理由：题材与广州/深圳岗位地理贴合，且「胡牌判定算法」是能白板手写的算法题，面试可讲。

---

## 1. 运行环境（不要自己再建 venv）

- 解释器：`C:/Users/SumMi/Documents/agent-learning/fastapi/.venv/Scripts/python.exe`（Python 3.12.3）
- 已装：fastapi 0.162.0 / pydantic 2.14.2 / uvicorn 0.54.0 / pytest 8.x / httpx / ruff / pydantic-settings
- 装新依赖务必用：`uv pip install --python .venv/Scripts/python.exe <pkg>`
- 测试命令：`.venv/Scripts/python.exe -m pytest -q`
- Lint 命令：`.venv/Scripts/python.exe -m ruff check app tests`

## 2. 目标目录结构

```
fastapi/
├── app/
│   ├── __init__.py
│   ├── main.py                  # B: create_app()
│   ├── core/
│   │   ├── __init__.py
│   │   ├── config.py            # B: Settings(pydantic-settings)
│   │   └── errors.py            # B: 业务异常 + 异常处理器
│   ├── schemas/
│   │   ├── __init__.py
│   │   ├── mahjong.py           # B: 麻将相关 Pydantic 模型
│   │   └── calc.py              # B: 计算器模型
│   ├── api/
│   │   ├── __init__.py
│   │   ├── router.py            # B: 汇总 /api/v1
│   │   ├── deps.py              # B: get_store / get_settings
│   │   └── routes/
│   │       ├── __init__.py
│   │       ├── health.py        # B
│   │       ├── calc.py          # B
│   │       ├── mahjong.py       # B
│   │       └── games.py         # B
│   └── services/
│       ├── __init__.py          # B
│       ├── store.py             # B: 内存 GameStore（asyncio.Lock, 上限, TTL）
│       └── mahjong/             # ★ 工程 A 独占
│           ├── __init__.py
│           ├── tiles.py         # 牌定义/牌墙/洗牌/发牌
│           └── rules.py         # 胡牌判定/听牌
├── tests/
│   ├── test_mahjong_tiles.py    # A
│   ├── test_mahjong_rules.py    # A
│   ├── test_api_mahjong.py      # B
│   ├── test_api_games.py        # B
│   └── test_api_calc.py         # B
├── docs/                        # Lead 独占
├── pyproject.toml               # B
├── Dockerfile / docker-compose.yml / .dockerignore   # B
├── .github/workflows/ci.yml     # B
├── .env.example                 # Lead
└── README.md                    # Lead
```

**写权限边界（严禁越界改别人的文件）**
- 工程 A：`app/services/mahjong/**`、`tests/test_mahjong_*.py`
- 工程 B：`app/__init__.py`、`app/main.py`、`app/core/**`、`app/schemas/**`、`app/api/**`、`app/services/__init__.py`、`app/services/store.py`、`tests/test_api_*.py`、`pyproject.toml`、`Dockerfile`、`docker-compose.yml`、`.dockerignore`、`.github/**`
- Lead：`docs/**`、`README.md`、`.env.example`、最终集成与验收

`majiang.py`（旧脚本）保留不动，作为"改造前"对照；新代码不 import 它。

---

## 3. 牌的编码（两端必须一致）

- 序数牌：`"1m"`~`"9m"`（万）、`"1p"`~`"9p"`（筒）、`"1s"`~`"9s"`（条）
- 字牌（沿用旧脚本 7 种，顺序固定）：`"E"` 东风、`"S"` 南风、`"W"` 西风、`"N"` 北风、`"P"` 白板、`"F"` 发财、`"C"` 红中
- 每种 4 张，共 34 种 × 4 = **136 张**；不含花牌
- 排序（展示/对比用）：`m → p → s → E S W N P F C`

## 4. 引擎接口（A 实现，B 只调用，签名不许改）

```python
# app/services/mahjong/tiles.py
TILE_KINDS: tuple[str, ...]          # 34 种，固定顺序
def build_deck() -> list[str]: ...            # 136 张
def shuffle_deck(deck: list[str], seed: int | None = None) -> list[str]: ...
def draw_tile(wall: list[str]) -> str | None: ...
def deal_hands(wall: list[str], seats: tuple[str, ...] = ("east","south","west","north")) -> dict[str, list[str]]:
    """每家 13 张，庄家（east）多摸 1 张共 14 张；牌墙不足抛 NotEnoughTilesError。"""
def sort_hand(hand: list[str]) -> list[str]: ...
def count_tiles(hand: list[str]) -> dict[str, int]: ...

# app/services/mahjong/rules.py
def winning_pattern(hand: list[str]) -> str | None:
    """14 张手牌 → "standard"（4 面子+1 将）| "seven_pairs"（七对）| None"""
def is_winning_hand(hand: list[str]) -> bool: ...
def waiting_tiles(hand: list[str]) -> list[str]:
    """13 张手牌 → 能胡的牌种列表（升序），空列表=不听牌"""

# 异常（放在 tiles.py，B 直接 import）
class MahjongError(ValueError): ...
class InvalidHandError(MahjongError): ...   # 张数不对 / 未知牌 / 同种超 4 张
class NotEnoughTilesError(MahjongError): ...
```

要求：纯函数、无 I/O、无全局可变状态、带 docstring 和类型注解；非法输入抛 `InvalidHandError`。

## 5. API 契约（B 实现，A 不碰）

统一前缀 `/api/v1`，JSON。错误统一信封：

```json
{"error": {"code": "HAND_INVALID", "message": "手牌张数必须是 13 或 14"}}
```

| 方法 | 路径 | 请求 | 成功响应 |
|---|---|---|---|
| GET | `/healthz` | — | `{"status":"ok","version":"0.1.0","uptime_seconds":123.4}` |
| GET | `/api/v1/mahjong/tiles` | — | `{"total":136,"kinds":34,"tiles":["1m",...]}` |
| POST | `/api/v1/mahjong/win-check` | `{"hand":["1m",...]}` | `{"is_win":true,"pattern":"standard"}` |
| POST | `/api/v1/mahjong/tenpai` | `{"hand":["1m",...13 张]}` | `{"waiting_tiles":["3m","6m"]}` |
| POST | `/api/v1/games` | `{"seed": 42}`（可省） | `{"game_id":"...","wall_remaining":83,"current_player":"east","hands":{...},"created_at":"..."}` |
| GET | `/api/v1/games/{game_id}` | — | 同上但不含完整他家和牌（`hands` 只给 east，其余给张数） |
| POST | `/api/v1/games/{game_id}/discard` | `{"seat":"east","tile":"3m"}` | `{"seat":"east","tile":"3m","wall_remaining":82,"discards":{...},"next_player":"south"}` |
| POST | `/api/v1/calc/{op}` | `{"a":1,"b":2}`，op ∈ add/subtract/multiply/divide | `{"op":"add","a":1,"b":2,"result":3}` |

约定：
- 计算除数为 0 → `400`，code=`DIVISION_BY_ZERO`
- 游戏不存在 → `404`，code=`GAME_NOT_FOUND`
- 手牌非法 / 打不出的牌 → `400`，code=`HAND_INVALID` / `TILE_NOT_IN_HAND`
- 请求体格式错误交给 FastAPI 默认 `422`
- `game_id` 用 `uuid4().hex`
- 游戏状态用 `app.state` 上的内存 `GameStore`，通过 `Depends(get_store)` 注入；`store` 用 `asyncio.Lock` 保护，最多 100 局，超过 2 小时未访问可清理
- 路由函数 `async def`，引擎调用是纯 CPU 的，保持同步调用但注明（面试可讲 event loop 取舍）
- CORS 允许全部来源（demo 用），并注明生产要收紧

## 6. 代码规范

- ruff 默认规则（line-length 100），全中文注释/文档字符串
- 所有公共函数有类型注解；不要在业务代码里用 `print`
- 日志用 `logging.getLogger(__name__)`，main.py 配一次 `basicConfig`
- 不要引入 requirements 之外的重依赖；不要用数据库（内存 store 即可，README 里写清"可替换 Redis/MySQL"）

## 7. 验收

```bash
.venv/Scripts/python.exe -m pytest -q          # 必须全绿
.venv/Scripts/python.exe -m ruff check app tests
.venv/Scripts/python.exe -m uvicorn app.main:app --port 8000
```
两个工程各自跑通自己的测试后再交付；Lead 负责端到端验收。
