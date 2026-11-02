# ADR-0003：Redis 对局仓库（第三个实现）与缓存演进

- 状态：已采纳
- 日期：2026-11-02
- 决策者：工程 F「Redis 存储实现」
- 相关文件：`app/services/repository/redis.py`、`tests/test_redis_repository.py`、
  `app/core/config.py`、`app/api/deps.py`、`app/main.py`、`docker-compose.yml`

---

## 1. 背景

目标岗位（广深后端实习）的 JD 里 **Redis 出现 4 次**，「熟悉 Redis 等常用缓存的使用」是高频
要求。仓库此时已有两个 `GameRepository` 实现：

- 内存 `GameStore`：零依赖、单进程，默认路径；
- SQLAlchemy 异步 `SqlGameRepository`：落库、重启可查，可切 MySQL。

再加第三个 **Redis 实现**，一方面让「接口抽象 → 换实现不改路由」的故事真正闭环，
另一方面补上 JD 的 Redis 缺口：Hash / EXPIRE / ZSET / `WATCH`+`MULTI` 这些面试
必问的 Redis 原语，在这个仓库里都有真实的落点，而不是背八股。

约束（与 ADR-0002 一致）：

1. **默认行为必须完全不变**：`REDIS_URL` 与 `DATABASE_URL` 都为空 → 内存实现；
2. 路由、schema、既有 139 个测试**一行不改**；
3. 三种实现的行为（同 seed 同牌局、错误码、TTL / LRU 语义）必须一致；
4. 本机没有真 Redis 服务、Docker daemon 也未启动 → 测试用 `fakeredis`，
   生产只改 `REDIS_URL`；真 Redis 的边界要**诚实写清楚**（见第 7 节）。

## 2. 决策一：Redis 直接当「对局仓库」，而不只是缓存

ADR-0002 第 6 节已经把「Redis 也能当仓库」写成了演进方向，本次落地。为什么值得单独实现：

- **接口已经抽象好了**：`GameRepository` 只有 4 个协程，实现 Redis 不需要碰路由，
  这正是 ADR-0002「依赖倒置」红利的事后验证；
- **对局是天然的 Redis 场景**：一次读写整份快照（聚合根）、需要 TTL、需要跨实例共享、
  需要原子 CAS——Hash + EXPIRE + ZSET + WATCH 刚好逐条对上；
- **面试可讲**：能讲清楚「什么时候用 Redis 当仓库、什么时候只当缓存」本身就是加分项。

**代价（必须主动说）**：Redis 的持久化强度弱于关系库（RDB 有快照间隔，AOF `everysec`
最多丢约 1 秒），也不适合复杂查询/统计。生产上「关系库为准 + Redis 做缓存」更常见，
第 8 节给出 cache-aside 演进路径；本实现的价值是演示「Redis 作为状态存储」的完整闭环。

## 3. 决策二：数据结构 = Hash 快照 + EXPIRE + 索引 ZSET

```text
HSET game:{game_id}
     wall=<json> hands=<json> discards=<json>
     current_player=east seed=42 created_at=2026-11-02T05:14:19+00:00
     last_access=1762... version=0
EXPIRE game:{game_id} 7200                     # 闲置 TTL，语义 = game_ttl_seconds
ZADD games:index {last_access} {game_id}       # LRU 上限 + 过期清理的索引
```

**为什么用 Hash 存整份快照，而不是一个 key 一个 field 或一个 key 一张牌：**

- 对局是聚合根：一次请求要牌墙 83 张 + 四家手牌 + 四家牌河，`HGETALL` 一次往返拿全；
  拆成 `tile:*` / 每座位一个 Hash 会退化成多次往返，且 `WATCH` 要盯多个 key，
  冲突概率和代码复杂度都上升；
- `wall` / `hands` / `discards` 是嵌套结构（`dict[seat, list[tile]]`），Hash 的 field
  只能是字符串，JSON 是最直接的序列化——和 SQL 实现的 `JSON` 列、内存实现的 dataclass
  **完全同构**，三种实现共享 `base.py` 里的同一份纯函数。

**为什么 TTL 直接用 `EXPIRE`，不新增 `CACHE_TTL_SECONDS`：**

对局仓库的「过期」和缓存的「过期」是两件事。仓库的 TTL 语义必须与内存 / SQL 实现一致，
所以直接复用 `game_ttl_seconds`；再引一个 `cache_ttl_seconds` 只会出现「Hash 还没过期、
索引已过期」这种两套时钟打架的 bug。将来真做 cache-aside 时，缓存 TTL 才需要单独配置
（取 `min(game_ttl_seconds, 300)`，永远比库短）。

## 4. 决策三：`WATCH` + `MULTI/EXEC` 做打牌乐观锁

一次打牌 = 读快照 → 校验 → 改三处（手牌 / 牌河 / 牌墙）→ 写回，必须防并发覆盖：

```python
async with client.pipeline(transaction=True) as pipe:
    await pipe.watch(key)                     # 1. 盯住这一局
    raw = await pipe.hgetall(key)             # 2. WATCH 之后的读走同一连接
    if not raw:
        raise GameNotFoundError(game_id)
    state = _decode_state(raw, game_id)
    next_player = apply_discard(state, seat, tile)   # 3. 纯函数：校验 + 状态迁移
    pipe.multi()                                     # 4. 进入事务
    pipe.hset(key, mapping=_encode_state(state))
    pipe.expire(key, ttl)
    pipe.zadd(INDEX_KEY, {game_id: now})
    await pipe.execute()      # 5. 期间有人改过 key → WatchError → 409 GAME_CONFLICT
```

- **失败即失败**：`WatchError` 抛 `ConcurrentUpdateError`，统一信封映射 409
  `GAME_CONFLICT`，绝不静默覆盖；
- **不做服务端自动重试**：打牌不是幂等操作（重试会重新摸牌），正确姿势是客户端拿 409
  后重新 `GET` 最新状态再决策；服务端无脑 `while True` 重试在热点对局上就是**重试风暴**。
  若真要自动重试，必须加「最大次数 + 指数退避 + 抖动」，并用版本号/幂等 token 防重放；
- **不用分布式锁（`SETNX` + 租约）**：锁要处理续期、死锁、进程崩溃后的锁残留，成本更高；
  单 key 的 CAS 用 `WATCH` 更轻，且和 SQL 实现的 `version` 乐观锁是同一个心智模型；
- **不用 Lua 脚本**：Redis 里用 `EVAL` 做 CAS 更省往返，但 `fakeredis` 默认不带 Lua 运行时
  （需要额外装 `lupa`），本项目坚持「不新增测试依赖」，所以选择 `WATCH/MULTI/EXEC`。
  真上生产可以换成 Lua，接口不变。

`get()` 是「读 + 续期」：只做 `EXPIRE` + `ZADD`（幂等），不 `WATCH`、也不重写 Hash——
这样即使 key 恰好在读与续期之间过期，`EXPIRE` 返回 0，不会把一份半残快照「复活」。

## 5. 决策四：LRU 上限与过期清理用 ZSET

`games:index`：member = `game_id`，score = 最近访问的 Unix 时间戳。

| 需求 | Redis 命令 | 说明 |
|---|---|---|
| 访问续期 / 挪到最新 | `ZADD games:index {now} {id}` | `get` 与 `discard` 都刷新 score |
| 超上限淘汰最旧 | `ZCARD` 判断 + `ZPOPMIN` | `MULTI` 内弹 score 最小的若干成员，再删对应 Hash |
| 批量清理过期 | `ZRANGEBYSCORE -inf {now-ttl}` | 删 Hash + `ZREM` 索引项 |

为什么用 ZSET 而不是 List：需要「按时间排序 + 随机删除 + 范围查询」三件事同时成立，
ZSET 一次全满足；List 只能两端进出，`get()` 刷新 LRU 就得先删再插，O(N) 且并发下容易乱。

细节：

- **清理是惰性的**：每次 `create` / `get` / `discard` 前顺手跑一次 `_purge_expired()`，
  不需要额外的定时任务（真实系统会再配一个后台清理任务兜底）；
- **索引残留兜底**：`ZPOPMIN` 弹索引与删除 Hash 是两步，进程如果在两步之间崩溃，
  Hash 会由 `EXPIRE` 自动回收，索引项则被下一次 `_purge_expired` 扫掉；
- **`clear()` 用 `SCAN` 不用 `FLUSHDB`**：生产 Redis 往往与缓存、限流共用实例，
  `FLUSHDB` 会误伤别人；`SCAN` + 批量 `DEL` 只删 `game:*` 前缀（只用于测试 / 运维，
  O(N) 可接受）。

## 6. 与内存 / SQL 实现的一致性怎么保证

1. **规则不重复实现**：发牌 `new_game_state`、打牌 `apply_discard` 全部复用
   `app/services/repository/base.py` 的纯函数。Redis 实现只负责「怎么存取」：
   `_encode_state` / `_decode_state` 是唯一与存储格式相关的代码。
2. **快照同构**：三种实现读出来的都是同一个 `GameState` dataclass。
3. **测试对照**（`tests/test_redis_repository.py`）：

   - 同 seed=42 → 内存 / SQL(SQLite 内存库) / Redis 的手牌、牌墙**逐字段相等**；
   - 同 seed=7 + 相同打牌序列（东家打一张、南家打一张）→ 三份快照的
     `wall/hands/discards/current_player` 完全相等；
   - 错误码对齐：`HAND_INVALID` / `TILE_NOT_IN_HAND` / `NOT_YOUR_TURN` / `GAME_NOT_FOUND`；
   - 失败的打牌不落任何状态（版本号、牌河、牌墙都不变）。

4. **配置优先级**（`app/api/deps.py` 的 `select_storage`）：
   `REDIS_URL` > `DATABASE_URL` > 内存；两者都配时日志会打印实际选中的实现，
   避免「以为在用 Redis」。

## 7. 测试边界：fakeredis 测了什么、没测什么

本机没有真 Redis 服务，Docker daemon 也未启动，所以：

**fakeredis 覆盖到了（13 个新测试，全绿）**：

- `create/get/discard/clear` 全流程 + 404/400 语义；
- 打牌：牌进牌河、牌墙递减、轮转下家、下家摸牌、总牌数守恒 136；
- **三种实现一致性**（同 seed 同牌局 / 同打牌序列同快照）；
- TTL：`ttl_seconds=1` 建局，真实等待 1.2 秒后 `get` → `GameNotFoundError`；
- LRU：`max_games=2` 建 3 局淘汰最旧；访问后刷新 LRU 位置；
- **并发**：15 个并发打同一张牌 → 1 次成功、14 次 `ConcurrentUpdateError`(409)，
  牌河只多 1 张、总牌数守恒；测试里用「WATCH 读完后 `asyncio.sleep(0)`」把并发窗口
  放大到必定重叠（fakeredis 是纯内存，await 不一定真正挂起）；
- 全链路：`create_app(Settings(redis_url=...))` + `TestClient` + 注入 fakeredis，
  走真实 HTTP 建局 / 查询 / 打牌 / 错误信封。

**没测到（诚实说明）**：

- **真 Redis 未实测**：网络分区、主从切换 / Sentinel、Cluster 分片、连接池耗尽、
  `maxmemory` 淘汰策略与业务 `EXPIRE` 的交互、AOF 重放后的过期精度；
- **多进程 / 多实例**：所有测试都在单进程内用同一个 fakeredis 实例；
- **性能**：没有压测；一次 `discard` 实际有 3~4 次往返（purge + WATCH + HGETALL + EXEC），
  热点对局会放大 RTT；
- **认证 / TLS / ACL**：`REDIS_URL` 支持带密码，但没有起过带 `requirepass` 的真实例；
- **WATCH 语义差异**：fakeredis 与真 Redis 在 pipeline reset / 连接复用边界上可能有细节差异，
  真实环境建议再补一个 `MYSQL_TEST_URL` 那样的 `REDIS_TEST_URL` 集成测试（本次未做）。

`docker compose config` 只做静态校验（不启 daemon）；`redis:7-alpine` 服务 + AOF 卷 + 
`redis-cli ping` 健康检查已经写进 `docker-compose.yml`，本地可 `docker compose up redis`
后用 `REDIS_URL=redis://127.0.0.1:6379/0` 真跑。

> 注意：API 镜像按 `pyproject.toml` 主依赖构建，`redis` 在可选 extra `[redis]` 里，
> 所以容器内的 API 默认仍是内存模式；要容器里也走 Redis，需要构建时安装 `[redis]`
> 并把 `REDIS_URL` 传给 api 服务。本次需求限定「只加服务、不改现有服务定义」，
> 因此没有动 api 服务；本地 demo 用 venv 起 API 即可。

## 8. 已知限制与竞态清单（面试主动说）

1. **跨实例时钟**：`last_access` 用应用侧 `time.time()`，多实例时钟不同步时 LRU / TTL
   判断会偏；生产应统一 NTP，或改用 Redis `TIME` 命令的服务器时钟。
2. **清理 / 淘汰的检查-删除窗口**：`_purge_expired` 与 `_evict_overflow` 都先 `WATCH`
   索引再判断，并发写者刷新分数会让本轮操作整体放弃（下一次再试），不会误删热点对局；
   但极端情况下会出现「该淘汰的没淘汰、暂时超过 `max_games`」，属可接受的最终一致。
3. **score 相同**：同一微秒创建的对局 score 相同，`ZPOPMIN` 会按 member 字典序弹出，
   不是严格 LRU；要严格单调可以给 score 拼一个递增序号（如 `INCR` 计数器）。
4. **`get()` 没有 `WATCH`**：读与续期之间可能读到「打牌前」的快照，这是允许的语义
   （客户端拿到的是某一时刻的一致快照），不需要为读争锁。
5. **`clear()` 是 O(N) 的 `SCAN`**：只用于测试 / 运维，不放进请求路径。
6. **连接池参数未调**：`Redis.from_url` 用默认值，生产要按 `workers × 并发` 调
   `max_connections`；也还没做启动 `PING` 的 fail-fast（Redis 挂了应用仍能启动，
   首个请求才会 500）。
7. **镜像里没有 redis 包**：见第 7 节的部署说明。

## 9. 将来怎么演进成 cache-aside

本实现已经是「Redis 当主存储」的形态。更常见的生产形态是「关系库为准 + Redis 缓存」，
演进步骤：

1. **读路径**：`GET game:{id}` → 命中直接反序列化返回；未命中读 SQL → `SETEX`
   （TTL = `min(game_ttl_seconds, 300)`，比库短，避免缓存活得比库久）；
2. **写路径**：SQL 事务**提交后**再 `DEL game:{id}`（先写库后删缓存；反过来会读到脏数据），
   或「写后更新 + 版本号只增」：缓存值带 `version`，只有新版本 ≥ 缓存版本才覆盖（Lua 原子）；
3. **击穿 / 雪崩**：热点对局用 `SETNX` 互斥回源（单飞），TTL 加随机抖动；
4. **穿透**：不存在的 `game_id` 缓存空值 + 短 TTL（注意别把 404 缓存太久）；
5. **最终一致**：删缓存失败时靠「延迟双删」或订阅 binlog（Canal）补偿；
6. **读写分离/归档**：对局结束写一条 `games_archive` 行 + 删除 Redis 快照，
   Redis 只保留活跃对局（`max_games` 从「进程上限」变成「活跃对局上限」）。

接口层面这套演进不需要动路由：缓存逻辑收敛在 repository 内部（装饰器 / 组合模式），
`GameRepository` 的四个协程签名不变。

## 10. 面试话术（3~5 句）

1. 「项目里对局状态抽象成 `GameRepository`，内存 / SQLAlchemy / Redis 三个实现，路由一行不改；
   Redis 实现用 Hash 存整份快照、`EXPIRE` 做 TTL、ZSET 做 LRU 上限，同 seed 三种存储发出同一副牌。」
2. 「打牌用 `WATCH + MULTI/EXEC` 乐观锁：读到的快照被并发改过就 `EXEC` 失败，返回 409
   `GAME_CONFLICT`，绝不静默覆盖；服务端不自动重试，避免热点对局上的重试风暴。」
3. 「一致性靠把发牌 / 打牌抽成纯函数，三种实现共用，测试直接对照三份快照；
   所以换存储不会换行为，这也是接口优先的最大收益。」
4. 「测试用 fakeredis 覆盖了并发冲突、TTL、LRU 和 HTTP 全链路，但真 Redis 的主从切换、
   网络分区、Cluster 没实测，这是我知道的测试边界，生产建议补 `REDIS_TEST_URL` 集成测试。」
5. 「下一步是 cache-aside：读穿透缓存、写库后删缓存，用版本号防并发覆盖、TTL 兜底，
   缓存逻辑收敛在 repository 里，路由仍然不用改。」

## 11. 验收（本次交付时实测）

```bash
# 全量测试：原有 139 + Redis 分支新增 13 = 152 passed
# （另有并行 MySQL 分支的 10 个集成测试在未配置 MYSQL_TEST_URL 时 skip）
.venv/Scripts/python.exe -m pytest -o addopts="" -q
# 152 passed, 10 skipped

# Lint
.venv/Scripts/python.exe -m ruff check app tests
# All checks passed!

# compose 静态校验（不需要 daemon）
docker compose config
# 通过：services 含 api + redis，volumes 含 redis-data

# 内存模式冒烟（默认零依赖）
curl /healthz                 # 200
POST /api/v1/games {"seed":42} # wall_remaining=83
POST .../discard               # wall_remaining=82, next_player=south

# Redis 模式冒烟：create_app + TestClient + fakeredis
# 启动日志 storage=redis(redis://127.0.0.1:6379/0)，建局→查询→打牌→404/400 全部通过
```
