# ADR-0004：对局读缓存（cache-aside）

- 状态：已采纳
- 日期：2026-11-02
- 决策者：工程 F（cache-aside 读缓存层）
- 相关文件：`app/services/cache/__init__.py`、`app/services/cache/cache_aside.py`、
  `tests/test_cache_aside.py`、`app/core/config.py`（Lead 维护的 `cache_redis_url` /
  `cache_ttl_seconds`）、`app/main.py`（接线由 Lead 维护）

---

## 1. 背景

本仓库已经有一条完整的「Redis 当**存储**」分支（ADR-0003）：`RedisGameRepository`
用 Hash 存整份快照、`WATCH/MULTI` 做乐观锁、ZSET 做 LRU。但目标岗位 JD 里的 Redis
通常是**缓存**语境（「熟悉 Redis 等常用缓存的使用」），两者能力模型不同：

| 角色 | 真相在哪 | 丢了会怎样 | 本仓库 |
|---|---|---|---|
| Redis 当存储 | Redis 自己 | 对局丢失 | `repository/redis.py`（ADR-0003） |
| Redis 当缓存 | 内存 / SQL / Redis 源仓库 | 回源重新加载，无数据损失 | **本 ADR** |

因此新增一层 **cache-aside 读缓存**：`CachedGameRepository(source, client, ttl_seconds)`
是对任意 `GameRepository` 的**装饰器**——它自己也实现同一个接口，路由层零改动
（与 ADR-0002「依赖倒置」一脉相承）。配置上由 Lead 新增了独立的
`CACHE_REDIS_URL`（缓存用 Redis，与 `REDIS_URL` 的存储角色分开）和
`CACHE_TTL_SECONDS`（默认 30，0 = 关闭）。

## 2. 决策一：为什么选 cache-aside，而不是 read-through / write-behind

| 方案 | 做法 | 为什么没选（对本项目） |
|---|---|---|
| **cache-aside（选它）** | 业务代码自己读缓存、miss 回源、写库后删缓存 | 回源 / 降级 / 失效时机都在应用手里，最好测、最好讲；缓存只是可选加速层 |
| read-through | 缓存库自己负责回源（如 Spring Cache 的 `@Cacheable`） | 相当于把 cache-aside 的「miss 回源」塞进缓存组件，要额外抽象 loader 注册；本项目 `CachedGameRepository` 已经是最小 read-through，没必要引入框架 |
| write-behind | 写只进缓存，异步批量刷库 | 对局状态要求「打牌立刻生效」，异步回写会丢数据、也会让并发打牌的版本号语义崩掉；只有计数 / 点赞这类可丢场景才适合 |
| write-through（全量） | 每次写都同步更新缓存 | 打牌是高频写，缓存会变成写热点；而且并发写的顺序可能反转（A 写库 v1、B 写库 v2，缓存被 v1 覆盖）→ 见第 3 节 |

**结论**：读走 cache-aside，写走「写库 + 删缓存」，只有 `create` 做一次轻量
write-through（理由见下）。

## 3. 决策二：读 / 写路径

```python
async def get(self, game_id):
    cached = await self._read_cache(game_id)      # GET cache:game:{id}
    if cached is not None:
        self._hits += 1
        return cached
    self._misses += 1
    state = await self._source.get(game_id)       # 未命中回源（404 原样抛）
    await self._write_cache(state)                # SET ... EX ttl；空结果不缓存
    return state

async def discard(self, game_id, seat, tile):
    state = await self._source.discard(...)       # 1. 先写源（提交成功）
    await self._delete_cache(game_id)             # 2. 再删缓存（写后删）
    return state
```

**为什么是「写后删」而不是「写时更新」**：

1. **并发写会写反**：两个打牌请求 A/B 几乎同时完成，如果各自「写缓存」，B 的
   `SET` 可能先执行、A 的后执行，缓存里留下旧状态；而「删」是幂等的，谁后删都安全，
   最坏只是多一次 miss；
2. **写放大最小**：删一个 key 是 O(1)，更新要序列化整份快照（牌墙 + 四家手牌 + 牌河，
   几 KB），而读命中率并不会因为「写时更新」显著提高（对局是读多写少，但每次都写整份）；
3. **失效失败兜底**：删失败还有 TTL 兜底（最长脏 30 秒）；写缓存失败则可能长期不一致。

**为什么 create 反而做 write-through**：客户端拿到 `game_id` 后几乎必然立刻 `GET`
一次（要完整手牌），预写缓存把「建局后必有一次 miss」消掉；而缓存内容就是刚提交的
状态、不存在并发写，所以没有写时更新的乱序问题。写失败只记 warning，不影响建局。

**顺序不能反**：如果先 `DEL` 再写库，会有「删缓存 → 并发读回填旧值 → 写库提交」的
经典窗口，缓存里会长期留旧值。本实现最多让缓存比库旧一个「读回填」窗口（第 7 节）。

## 4. 决策三：TTL 30 秒的取舍

`cache_ttl_seconds` 默认 30s（Lead 配置，0 = 关闭），取值逻辑：

- **太长**：失效路径出 bug（删缓存失败、读回填竞态）时，脏数据最长要等 TTL 才消失；
  对局状态是用户正在打牌的状态，30 秒已经能感知到「我明明打了牌怎么又回来了」；
- **太短**：命中率下降、源库压力回升，缓存白加；
- **30s 的直觉**：一个对局的读频率大概是「查看一次 → 考虑几秒 → 打一张」，30 秒覆盖
  大部分重复读；同时给失效兜底留一个用户可接受的上限。

**TTL 抖动**：真实系统应写成 `ttl = base * (1 + random.uniform(-0.1, 0.1))`，避免大批
对局在同一秒过期造成「雪崩」；对局 id 是 `uuid4`，创建时间天然分散，抖动不是当前
主要矛盾，留作演进项（第 6 节）。

## 5. 决策四：可用性（降级）与可观测性

- **缓存故障必须降级**：`redis.asyncio.Redis` 的 `ConnectionError` / `TimeoutError` /
  任意缓存异常都被捕获，`logger.warning` + `errors += 1`，然后直接走 `source`。
  缓存是加速层，Redis 挂了最多是「读变慢」，**不能把 500 抛给用户**；
- **脏缓存也要降级**：缓存值不是合法 JSON、缺字段、字段类型不对、`game_id` 与 key
  不匹配（串局）时，`errors += 1`、删掉脏值、按未命中回源；
- **可观测性**：`hits` / `misses` / `errors` 三个计数器 + `hit_rate` 命中率，
  demo 时可以直接展示「缓存有没有用」；生产可以接 Prometheus（`errors` 上升就告警）；
- **缓存关闭**：`ttl_seconds=0` 时 `enabled=False`，所有方法直通 `source`，
  连坏客户端都不会被调用（测试里用一个「每次操作都抛异常」的客户端验证）。

## 6. 决策五：序列化与 key 隔离

- **缓存值是一条扁平 JSON 文档**，字段与 `GameState` 一一对应，`created_at` 用
  ISO 8601 带时区字符串。没有复用 `repository/redis.py` 的 Hash 字段编码：那份编码是
  私有的（`_encode_state` / `_decode_state`）且面向「一个 Hash 多个 field」，缓存是
  「一个 key 一个 JSON」，复用会把两层 JSON 套在一起、也把缓存格式和存储格式绑死。
  若以后要统一，正确做法是把编解码抽到中立模块（`base.py` 或 `codec.py`），由两边
  import——但那是 Lead / 存储分支的写权限，本分支不做。
- **key 前缀 `cache:game:`**：故意与 Redis 存储的 `game:` 隔开。生产里缓存和存储
  可能共用同一个 Redis 实例，前缀混用会互相覆盖；测试里也验证了
  `cache:game:{id}` ≠ `game:{id}`，且 `clear()` 只清自己前缀（其他前缀 key 保留）。
- **清缓存用 `SCAN` 前缀遍历 + 批量 `DEL`**，不用 `KEYS *`（阻塞 Redis）或 `FLUSHDB`
  （误伤同实例其他业务数据）。

## 7. 穿透 / 击穿 / 雪崩，在这个场景怎么防

| 问题 | 场景 | 本实现 | 演进 |
|---|---|---|---|
| **缓存穿透** | 拿不存在的 `game_id` 高频查，缓存永远 miss、全打到源 | **空结果不缓存**（`GameNotFoundError` 直接抛，不写 `null`）——这是 Lead 明确要求的策略；也避免「缓存了空值、之后对局才创建」的脏空值 | 若线上真被打，缓存短 TTL（1~5s）的空值占位，或用布隆过滤器挡不存在的 id |
| **缓存击穿** | 单个热点对局 TTL 到期瞬间，大量请求同时回源 | 未实现单飞；单进程可加 `asyncio.Lock` 按 `game_id` 分段，多实例用 `SETNX` 互斥回源 | 逻辑过期（value 带 `expire_at`，后台异步刷新，读永远不阻塞）+ 互斥锁 |
| **缓存雪崩** | 大批 key 同一时刻过期 / Redis 整体挂 | 对局 `uuid4` 创建时间天然分散；Redis 挂时全量降级回源（第 5 节），不会 500 | TTL 加 ±10% 随机抖动、多级缓存、源库限流、Redis 哨兵/集群 |

## 8. 多实例一致性与已知竞态（面试主动说）

1. **读-回填竞态（最经典的 cache-aside 漏洞）**：实例 A 读 miss → 读源拿到旧值 →
   实例 B 写源并删缓存 → A 把旧值 `SET` 回缓存 → 缓存脏到 TTL 过期。
   本实现最长不一致窗口 = TTL（30s）。缓解手段（未实现，写进演进）：
   - 缓存值带 `version`，回填时用 Lua「只有新版本才覆盖」；
   - 延迟双删（写后删一次，几百 ms 再删一次）；
   - 源库 binlog 订阅（Canal）驱动删缓存。
2. **删缓存失败**：写库成功、`DEL` 失败 → 脏到 TTL；`errors` 计数会暴露。
3. **并发打牌**：缓存层不参与写并发——写路径直接调 `source.discard()`，并发控制由
   源实现负责（内存 `asyncio.Lock` / SQL 行锁 + version CAS / Redis `WATCH`，见 ADR-0002、
   ADR-0003）。两个并发 discard 都写库成功、各自删一次缓存，最终缓存为空，
   下一次读回源拿最新，安全。
4. **多实例共享缓存**：删缓存是全局操作，一个实例写库删缓存后其他实例读 miss 回源，
   能拿到新值；但**没有跨 Redis 与源库的分布式事务**，只能做到最终一致（TTL 兜底）。
   如果源是 SQL、缓存和库之间网络分区，删缓存和写库无法原子，这正是「最终一致」的边界。
5. **缓存里 `last_access` 是旧值**：命中缓存时不回源，源库的闲置 TTL 不会被刷新；
   由于缓存 TTL（30s）远小于对局 TTL（7200s），每 30 秒内必有一次回源刷新，
   不会造成对局被误清理。这个字段也不参与任何一致性判断。

## 9. 测试：覆盖了什么、fakeredis 没测到什么

**14 个测试（`tests/test_cache_aside.py`，fakeredis 注入，`asyncio.run` 包裹）**：

1. 未命中回源后第二次命中、源只读一次；
2. 缓存返回快照与源直读逐字段一致（含 `datetime` 时区）；
3. 不存在的对局不缓存空结果、两次都回源；
4. TTL（1s）过期后重新回源；
5. 打牌后缓存被删、下次读到新状态（牌墙 82、牌河 +1、轮转南家）；
6. 建局 write-through 后立刻查询命中、缓存带 TTL；
7. 缓存客户端每次操作抛 `ConnectionError` → 业务全部降级走源、`errors=7`、无 500；
8. hits / misses / errors / hit_rate 计数正确；
9. `ttl_seconds=0` 完全直通源、坏客户端调用次数为 0；
10. 非法 JSON / `game_id` 不匹配的脏缓存按未命中处理并覆盖；
11. 缓存 key 前缀与 Redis 存储 key 隔离；
12. `clear()` 清源 + 清自己的前缀，其他前缀 key 保留；
13. `ttl_seconds < 0` 构造时抛 `ValueError`；
14. HTTP 全链路：`create_app(Settings(redis_url=..., cache_ttl_seconds=30))` + `TestClient`，
    测试里手工接线（`main.py` 由 Lead 接线），建局 → 查询 miss → 再查询 hit → 打牌后
    删缓存 → 查询拿到新值 → 再查询 hit。

交付时全量：`197 passed, 11 skipped`（基线 163 + 本分支 14 + 并行分支新增；11 skipped
是 MySQL 集成测试在无 `MYSQL_TEST_URL` 时的门控），`ruff check app tests` 干净。

**fakeredis 没测到什么（诚实说明）**：

- **真网络行为**：连接超时、断线重连、主从切换 / Sentinel / Cluster、跨机房延迟；
  降级测试是「注入必然抛异常的客户端」，验证了代码路径，但没有验证真 Redis 挂掉时的
  `ConnectionError` 类型与耗时；
- **真 TTL 精度**：fakeredis 用进程内时钟，和真 Redis 的秒级过期粒度/惰性删除不同；
- **`SCAN` 规模性能**：百万 key 下的扫描耗时、批量 `DEL` 的阻塞；
- **真并发**：测试是单进程 asyncio，没有多进程 / 多实例同时读写同一对局；
- **缓存收益**：没做压测，`hit_rate` 只证明逻辑正确，不证明生产 QPS 提升；
- **与 SQL 源的组合**：装饰器与 source 无关，测试里用了内存与 Redis 存储作源；
  没有在缓存 + MySQL 组合下跑集成测试（MySQL 分支的 `MYSQL_TEST_URL` 门控测试未覆盖缓存层）。

## 10. 接线与部署

- **配置**：`CACHE_REDIS_URL` 非空且 `CACHE_TTL_SECONDS > 0` 时启用；`REDIS_URL` 是
  「Redis 当存储」，两者角色不同，**不要混用同一个连接串语义**（可以用同一个 Redis
  实例，但要用不同 db 或至少不同 key 前缀——本实现前缀已隔离）；
- **接线**：`lifespan` 里 `source = 现有仓库`，再
  `application.state.store = CachedGameRepository(source, cache_client, settings.cache_ttl_seconds)`，
  关闭时 `aclose()` 缓存 client。这部分在 `app/main.py`，**由 Lead 负责**（本分支写权限
  不含 main.py）；测试里的 `test_API闭环_*` 用同样方式在测试内手工接线，证明路由零改动可用；
- **依赖**：`redis` 仍是 `pyproject.toml` 的可选 extra；`cache_aside.py` 只依赖
  duck-typed 的 `AsyncCacheClient`（`get/set/delete/scan_iter`），不 import 具体客户端，
  因此内存模式 / 未装 redis 的部署不会因为 import 缓存模块而失败（前提是没人接线）；
- **观测**：`hits/misses/errors/hit_rate` 已经挂在仓库对象上，Lead 若加 `/metrics`
  或启动日志可直接读取。

## 11. 面试话术（3~5 句）

1. 「项目里有两条 Redis 分支：一条是『Redis 当存储』（Hash + WATCH 乐观锁），另一条是
   『Redis 当缓存』——cache-aside 装饰器，读走缓存、写库后删缓存，路由一行不用改。」
2. 「写选『写后删』不选『写时更新』：并发写的顺序可能反转导致缓存留下旧值，删是幂等的；
   create 除外，做 write-through 是因为客户端建局后必然马上查，预写省掉一次 miss。」
3. 「缓存挂了必须降级：连接失败 / 超时 / 脏 JSON 都只记 warning 和 errors 计数、直接回源，
   不能因为缓存把 500 抛给用户；hits/misses/errors 还能算命中率。」
4. 「穿透用『空结果不缓存』，击穿要单飞或逻辑过期，雪崩靠 TTL 抖动；这些我实现了第一项，
   后两项在 ADR 里写了演进方案，没有过度设计。」
5. 「多实例下 cache-aside 只能最终一致：经典读回填竞态最长脏到 TTL（30s），要更强一致
   就上版本号比较 / 延迟双删 / binlog 订阅；fakeredis 测了逻辑和降级，真 Redis 的网络
   分区和主从切换我还没实测，这是已知测试边界。」

## 12. 验收（实测）

```bash
.venv/Scripts/python.exe -m pytest -o addopts="" -q tests/test_cache_aside.py
# 14 passed

.venv/Scripts/python.exe -m pytest -o addopts="" -q
# 197 passed, 11 skipped（基线 163 + 本分支 14 + 并行分支新增）

.venv/Scripts/python.exe -m ruff check app tests
# All checks passed!
```

全链路冒烟（`create_app(Settings(redis_url=..., cache_ttl_seconds=30))` + TestClient +
fakeredis，测试里手工接线）：

```text
create      -> wall_remaining=83  stats[hits=0 misses=0 errors=0 hit_rate=0.00]
get #1      -> 200  wall_remaining=83  east=14  stats[hits=0 misses=1 errors=0 hit_rate=0.00]
get #2      -> 200  wall_remaining=83  stats[hits=1 misses=1 errors=0 hit_rate=0.50]
discard     -> 200  wall_remaining=82 next=south  (cache key exists=0)
get after   -> 200  wall_remaining=82 current=south south=14  stats[hits=1 misses=2 errors=0 hit_rate=0.33]
get again   -> 200  stats[hits=2 misses=2 errors=0 hit_rate=0.50]
```
