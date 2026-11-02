# MySQL 5.7 实测报告：持久化分支从「代码支持」到「实测过」

- 日期：2026-11-02
- 执行者：工程 E「MySQL 实测」
- 被测对象：`app/services/repository/`（`SqlGameRepository` + `GameState` 映射）、`app/main.py` 的存储切换、`app/core/config.py` 的 `DATABASE_URL`
- 相关文档：`docs/adr-0002-持久化选型.md`（其中「本地不连真 MySQL」一节已被本报告取代）
- 新增文件：`tests/test_mysql_integration.py`（本报告的唯一代码交付）
- 临时证据：`.tmp/mysql-test/`（已被 `.gitignore` 第 46 行 `.tmp/` 忽略，不进仓库）

---

## 0. 一句话结论

> **在本机真实 MySQL 5.7.44 上，持久化分支跑通了：新建 11 个集成用例全绿、既有 18 个持久化用例里 12 个真打到 MySQL（11 通过、1 个概率性失败）、默认套件仍然零依赖、uvicorn + curl 端到端可建局打牌。**
>
> 实测同时暴露了 **两个 SQLite 上永远看不到的真问题**（都在 `app/**` 里，不在本工程写权限内，已上报 Lead 决定是否开工单）：
> 1. `last_access` 用 SQLAlchemy `Float` 映射到 MySQL 的 **4 字节 FLOAT**，2026 年的 epoch 秒会被量化到 **万秒级**，TTL/LRU 精度失真，既有 TTL 用例因此在 MySQL 上**概率性失败**；
> 2. `create()` 事务开头的 `_purge_expired`（`DELETE ... WHERE last_access < ?`）与并发 `INSERT` 在 `ix_games_last_access` 上**间隙锁死锁（1213）**，10 并发建局可稳定复现。

---

## 1. 环境（全部为命令原始输出）

### 1.1 软件版本

```text
$ "/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqld.exe" --version
C:\Program Files\MySQL\MySQL Server 5.7\bin\mysqld.exe  Ver 5.7.44 for Win64 on x86_64 (MySQL Community Server (GPL))
```

```text
mysql> SELECT @@version, @@version_comment, @@innodb_version, @@transaction_isolation;
+-----------+------------------------------+----------------+-----------------------+
| 5.7.44    | MySQL Community Server (GPL) | 5.7.44         | REPEATABLE-READ       |
+-----------+------------------------------+----------------+-----------------------+

mysql> SELECT @@sql_mode;
ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,
ERROR_FOR_DIVISION_BY_ZERO,NO_AUTO_CREATE_USER,NO_ENGINE_SUBSTITUTION
```

| 组件 | 版本 |
|---|---|
| OS | Windows（git-bash） |
| MySQL Server | **5.7.44**（Win64, x86_64, MySQL Community Server GPL） |
| InnoDB | 5.7.44，默认隔离级别 **REPEATABLE-READ** |
| Python | 3.12.3（`.venv/Scripts/python.exe`） |
| SQLAlchemy | **2.1.6**（`sqlalchemy[asyncio]`） |
| aiomysql | **0.3.2** |
| PyMySQL | **2.2.8** |
| aiosqlite | 已装（SQLite 分支继续使用） |

### 1.2 为什么起隔离实例（而不是用 3306）

本机 3306 已有一个 MySQL 实例在跑（PID 7056），**账号密码未知**，且里面是用户的真实数据。
本工程从第一分钟起就定死一条纪律：**不动 3306、不改它的数据、不需要它的任何权限**。
做法是在 `.tmp/mysql-test/` 下 `--initialize-insecure` 一个**全新 datadir + 3307 端口**的独立实例。

```text
$ netstat -ano | grep -E ":3306|:3307"
  TCP    0.0.0.0:3306    0.0.0.0:0    LISTENING    7056      ← 用户的实例，全程未动
  TCP    127.0.0.1:3307  0.0.0.0:0    LISTENING    13824     ← 本工程的隔离实例
```

### 1.3 建实例的完整命令（可复现）

```bash
cd /c/Users/SumMi/Documents/agent-learning/fastapi
mkdir -p .tmp/mysql-test/data

# 1) 初始化一个空 datadir（root 无密码，只对 127.0.0.1:3307 生效）
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqld.exe" \
  --initialize-insecure \
  --basedir="C:/Program Files/MySQL/MySQL Server 5.7" \
  --datadir="C:/Users/SumMi/Documents/agent-learning/fastapi/.tmp/mysql-test/data" \
  --console
```

初始化成功的关键输出（节选）：

```text
[Warning] InnoDB: New log files created, LSN=45790
[Warning] root@localhost is created with an empty password ! Please consider switching off the --initialize-insecure option.
```

```ini
# 2) .tmp/mysql-test/my.ini（显式 --defaults-file，避免误读 3306 实例的 my.ini）
[mysqld]
basedir=C:/Program Files/MySQL/MySQL Server 5.7
datadir=C:/Users/SumMi/Documents/agent-learning/fastapi/.tmp/mysql-test/data
port=3307
bind-address=127.0.0.1
server-id=3307
character-set-server=utf8mb4
collation-server=utf8mb4_general_ci
max_connections=200
# 不要开 skip-name-resolve：开了之后 127.0.0.1 不会被反解成 localhost，
# initialize-insecure 建的 root@localhost 就会拒绝连接（ERROR 1130）。
```

```bash
# 3) 启动（后台）
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqld.exe" \
  --defaults-file="C:/Users/SumMi/Documents/agent-learning/fastapi/.tmp/mysql-test/my.ini" \
  --console

# 4) 验证能连 + 建库
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysql.exe" \
  -h 127.0.0.1 -P 3307 -u root --protocol=tcp \
  -e "select version() as v, @@port as port, @@datadir as datadir, @@character_set_server as charset"
```

```text
v        port  datadir                                                          charset
5.7.44   3307  C:\\Users\\SumMi\\Documents\\agent-learning\\fastapi\\.tmp\\mysql-test\\data\\   utf8mb4
```

```bash
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysql.exe" -h 127.0.0.1 -P 3307 -u root --protocol=tcp \
  -e "CREATE DATABASE IF NOT EXISTS mahjong CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;"
```

### 1.4 踩到的两个坑（留给后人）

| 现象 | 原因 | 解法 |
|---|---|---|
| `ERROR 1130 (HY000): Host '127.0.0.1' is not allowed to connect to this MySQL server` | `my.ini` 里写了 `skip-name-resolve`，`127.0.0.1` 不再被反解成 `localhost`，而 `--initialize-insecure` 建的是 `root@localhost` | 去掉 `skip-name-resolve`（或手工建 `root@'127.0.0.1'`，但那时你根本连不上） |
| 启动时报端口被占用 / datadir 不对 | 不传 `--defaults-file` 时 mysqld 会去读 basedir、`%WINDIR%`、`%APPDATA%` 下的 `my.ini`，很可能读到 3306 实例的配置 | 永远显式传 `--defaults-file`，把 basedir/datadir/port 全部钉死 |

---

## 2. 跑通情况（每条都是真实跑出来的）

### 2.1 新增集成测试：`tests/test_mysql_integration.py` → 11 passed

```bash
MYSQL_TEST_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong" \
  .venv/Scripts/python.exe -m pytest -o addopts="" -q tests/test_mysql_integration.py
```

```text
...........                                                              [100%]
11 passed, 1 warning in 1.38s
```

11 个用例覆盖（对应任务要求的 5 条全覆盖）：

| # | 用例 | 覆盖点 |
|---|---|---|
| 1 | `test_在MySQL上建表且列类型符合预期` | 建表、`create_all` 幂等、真实列类型（`json` / `varchar(32)` / `datetime` / `float`）、`5.7.x` 版本断言 |
| 2 | `test_MySQL仓库完成创建查询打牌清空全流程` | 建局 → 查询 → 打牌 → 版本递增 → 牌数守恒 136 → clear 后 404 |
| 3 | `test_MySQL上应用重启后同一局仍可查询并继续打牌` | **重启（dispose 整个连接池 + 新 engine）后仍可查** —— 真持久化 |
| 4 | `test_MySQL并发打同一局只有一次成功且状态不错乱` | **并发打牌只有一次成功**（行锁串行化），最终状态自洽 |
| 5 | `test_MySQL上过期版本号的写回被CAS拦住并映射成409` | CAS 兜底：过期 `version` 的写回 `rowcount == 0` → 409 `GAME_CONFLICT` |
| 6 | `test_相同seed在MySQL与内存实现下发出同一副牌` | **同 seed 两种存储发牌一致**（换存储不换行为） |
| 7 | `test_MySQL的JSON列保留数组顺序且类型是原生json` | JSON 列行为（见 §4） |
| 8 | `test_MySQL的FLOAT列把epoch时间戳量化到万秒级` | **实测发现 #1**（见 §6.1） |
| 9 | `test_MySQL模式下API建局打牌与重启后查询` | 真实 HTTP 栈（TestClient + lifespan）+ 重启后继续打牌 |
| 10 | `test_MySQL并发建局遇1213死锁重试后全部成功` | **实测发现 #2**（见 §6.2） |
| 11 | `test_MySQL的DSN写法与密码掩码` | DSN 方言解析 + `mask_dsn` 掩码 |

**默认套件零影响**：不设 `MYSQL_TEST_URL` 时，文件顶部
`pytestmark = pytest.mark.skipif(not MYSQL_TEST_URL, ...)` 生效，11 个用例全部 skip：

```bash
$ .venv/Scripts/python.exe -m pytest -o addopts="" -q tests/test_mysql_integration.py
11 skipped, 1 warning in 0.72s
```

### 2.2 既有 18 个持久化用例：**不改测试文件**，重定向到 MySQL 跑

`tests/test_persistence.py` 里的 SQL DSN 是写死的
`_sqlite_url(tmp_path)`（`sqlite+aiosqlite:///<tmp_path>/games.db`），**设置 `DATABASE_URL`
环境变量对它没有任何影响**——它构造 `Settings(database_url=...)` 时全部显式传参。
所以「跑现有用例」必须另想办法。

用了一个**临时 pytest 插件**（`.tmp/mysql-test/mysql_shim_plugin.py`，不入库、不改测试文件）：
在 collection 结束后把模块级 `_sqlite_url` 换成本函数，**每个测试分配一个全新的 MySQL 库**
（`CREATE DATABASE shim_<pid>_<n>`，与 `tmp_path` 的隔离语义等价），session 结束 drop 掉。

```bash
PYTHONPATH="C:/Users/SumMi/Documents/agent-learning/fastapi/.tmp/mysql-test" \
MYSQL_TEST_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong" \
  .venv/Scripts/python.exe -m pytest -o addopts="" -q tests/test_persistence.py -p mysql_shim_plugin
```

```text
[shim] 已把 test_persistence._sqlite_url 重定向到 MySQL 127.0.0.1:3307
..................[shim] 本轮共创建 12 个临时 MySQL 库，准备 drop
18 passed, 1 warning in 2.10s
```

**18 个用例里真正打到 MySQL 的是 12 个**（插件为本轮创建了 12 个临时库，是硬证据）。
另外 6 个是：`test_内存数据库URL通过StaticPool也可用`（显式写死 `sqlite+aiosqlite:///:memory:`，
MySQL 没有对应的单连接内存库语义）+ 5 个与存储无关的用例（配置选择、接口继承、内存并发、DSN 掩码、SEATS）。

**多轮结果（诚实版）**：

```text
连跑 6 轮全量（每轮 18 个）： 18 passed × 6 轮
早期另外 4 轮全量：          1 failed, 17 passed（失败的只有 TTL/LRU 那一条）
单跑该 TTL 用例，第一批 12 次： 4 次通过、8 次失败
单跑该 TTL 用例，第二批  8 次： 8 次通过、0 次失败
```

失败断言：

```text
E   Failed: DID NOT RAISE GameNotFoundError
tests\test_persistence.py:294: Failed
```

即 `test_SQL实现遵守对局数上限与TTL清理` 在 MySQL 上**概率性失败**。
这不是「环境没配好」，而是 SQLite → MySQL 的真实类型差异，根因在 §6.1。
**结论：既有 18 个持久化用例在 MySQL 上 17～18 个原样通过，1 个因浮点精度问题概率性失败。**

### 2.3 默认套件（不设环境变量）：仍然零依赖

```bash
$ .venv/Scripts/python.exe -m pytest -o addopts="" -q
152 passed, 11 skipped, 1 warning in 3.64s
     ↑ 11 skipped 全是本报告新增的 MySQL 用例

$ .venv/Scripts/python.exe -m pytest -o addopts="" -q --ignore=tests/test_mysql_integration.py
152 passed, 1 warning in 3.80s      # 排除新增文件后完全一致
```

> 说明：任务书里写的是「139 passed」，但本工程执行期间其他工程并行合入了
> `tests/test_redis_repository.py` 等测试，基线已经涨到 152。
> 本报告新增的 11 个用例对默认套件的贡献是 **0 passed / 11 skipped**，两者互不影响。

### 2.4 静态检查

```bash
$ .venv/Scripts/python.exe -m ruff check app tests
All checks passed!
```

### 2.5 端到端：uvicorn + 真 MySQL + curl 建局/打牌

```bash
DATABASE_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong" \
  .venv/Scripts/python.exe -m uvicorn app.main:app --host 127.0.0.1 --port 8011 --log-level info
```

启动日志（存储确实是 MySQL，不是内存、不是 SQLite）：

```text
INFO:     Started server process [44796]
2026-11-02 13:16:13,973 | INFO    | app.main | 服务启动 name=广东麻将（推倒胡）后端服务 version=0.1.0
  storage=sql(mysql+aiomysql://root@127.0.0.1:3307/mahjong) max_games=100 ttl=7200s
INFO:     Application startup complete.
INFO:     Uvicorn running on http://127.0.0.1:8011 (Press CTRL+C to quit)
```

```text
$ curl -s http://127.0.0.1:8011/healthz
{"status":"ok","version":"0.1.0","uptime_seconds":6.484}

$ curl -s -X POST http://127.0.0.1:8011/api/v1/games -H "Content-Type: application/json" -d '{"seed": 42}'
{"game_id":"892ad8f42118498c9b4197fc2d36b9f3","wall_remaining":83,"current_player":"east",
 "hands":{"east":["2m","4m","6m","7m","8m","3p","6p","3s","6s","8s","E","P","P","C"],
          "south":["1m","2m","2m","5m","8m","9m","3p","6p","9p","5s","S","P","F"],
          "west":["1m","7m","7m","4p","5p","8p","9p","9p","9p","1s","7s","N","N"],
          "north":["3m","4m","4m","6m","9m","2p","5p","7p","2s","5s","8s","9s","F"]},
 "created_at":"2026-11-02T05:16:20Z"}

$ curl -s http://127.0.0.1:8011/api/v1/games/892ad8f42118498c9b4197fc2d36b9f3
{"game_id":"892ad8f42118498c9b4197fc2d36b9f3","wall_remaining":83,"current_player":"east",
 "hands":{"east":["2m","4m","6m","7m","8m","3p","6p","3s","6s","8s","E","P","P","C"],
          "south":13,"west":13,"north":13},
 "created_at":"2026-11-02T05:16:20Z"}

$ curl -s -X POST http://127.0.0.1:8011/api/v1/games/892ad8f42118498c9b4197fc2d36b9f3/discard \
       -H "Content-Type: application/json" -d '{"seat":"east","tile":"2m"}'
{"seat":"east","tile":"2m","wall_remaining":82,
 "discards":{"east":["2m"],"south":[],"west":[],"north":[]},"next_player":"south"}
```

服务端业务日志 + **直接查库核对**（证明是落库不是内存）：

```text
2026-11-02 13:16:20,495 | INFO | app.services.repository.sql | 创建对局（SQL） game_id=892ad8... seed=42 wall_remaining=83
2026-11-02 13:16:20,821 | INFO | app.services.repository.sql | 打牌（SQL） game_id=892ad8... seat=east tile=2m next=south wall_remaining=82 version=1

mysql> SELECT game_id, version, current_player, JSON_LENGTH(wall) AS wall_left,
              JSON_LENGTH(discards->'$.east') AS east_discards
       FROM games WHERE game_id='892ad8f42118498c9b4197fc2d36b9f3';
+----------------------------------+---------+----------------+-----------+---------------+
| game_id                          | version | current_player | wall_left | east_discards |
+----------------------------------+---------+----------------+-----------+---------------+
| 892ad8f42118498c9b4197fc2d36b9f3 |       1 | south          |        82 |             1 |
+----------------------------------+---------+----------------+-----------+---------------+
```

---

## 3. EXPLAIN 原始输出（10 万行数据）

表结构（`SHOW CREATE TABLE`，与 SQLite 那份 `Base.metadata` 完全同源）：

```sql
CREATE TABLE `games` (
  `game_id` varchar(32) NOT NULL,
  `seed` int(11) DEFAULT NULL,
  `wall` json NOT NULL,
  `hands` json NOT NULL,
  `discards` json NOT NULL,
  `current_player` varchar(8) NOT NULL,
  `created_at` datetime NOT NULL,
  `last_access` float NOT NULL,
  `version` int(11) NOT NULL,
  PRIMARY KEY (`game_id`),
  KEY `ix_games_last_access` (`last_access`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
```

造数：`CREATE TABLE games LIKE mahjong.games` 后批量插入 **100000 行**（10×10×10×10×10 的数字表
笛卡尔积，单条 `INSERT ... SELECT`，不污染业务库），`ANALYZE TABLE games` 后抓计划。

### 3.1 按主键查（`get()` 的读路径）

```sql
EXPLAIN SELECT * FROM games WHERE game_id = '00000000000000000000000000000042'\G
```

```text
           id: 1
  select_type: SIMPLE
        table: games
   partitions: NULL
         type: const
possible_keys: PRIMARY
          key: PRIMARY
      key_len: 130
          ref: const
         rows: 1
     filtered: 100.00
        Extra: NULL
```

`type=const`、`rows=1`、`key_len=130`（`varchar(32)` 在 utf8mb4 下是 32×4+2 = 130 字节），
说明 10 万行下也是**主键常数级查找**，不会退化成全表扫描。

### 3.2 version CAS 更新（`discard()` 的写路径）

```sql
EXPLAIN UPDATE games
   SET wall='["1m"]', hands='{"east": []}', discards='{"east": []}',
       current_player='south', last_access=1793596500, version = version + 1
 WHERE game_id = '00000000000000000000000000000042' AND version = 0\G
```

```text
           id: 1
  select_type: UPDATE
        table: games
   partitions: NULL
         type: range
possible_keys: PRIMARY
          key: PRIMARY
      key_len: 130
          ref: const
         rows: 1
     filtered: 100.00
        Extra: Using where
```

`type=range` + `rows=1`：`game_id` 先用主键做等值定位，`version` 在**定位到的那一行上**
用 `Using where` 过滤——这正是乐观锁想要的样子（**不需要**额外给 `version` 建索引，
否则是浪费：一局一行、按主键已经唯一确定）。

### 3.3 行锁读（MySQL 才有的分支）

```sql
EXPLAIN SELECT * FROM games WHERE game_id = '00000000000000000000000000000042' FOR UPDATE\G
```

```text
        table: games
         type: const
          key: PRIMARY
      key_len: 130
         rows: 1
        Extra: NULL
```

`FOR UPDATE` 不影响访问路径（仍是主键 const），只改变**加锁方式**：对命中的那一行加排他记录锁。

### 3.4 顺带：TTL 清理与 LRU 淘汰也走索引

```sql
EXPLAIN DELETE FROM games WHERE last_access < 1793596500\G
```

```text
        table: games
         type: range
possible_keys: ix_games_last_access
          key: ix_games_last_access
      key_len: 4
          ref: const
         rows: 208
        Extra: Using where
```

```sql
EXPLAIN SELECT game_id FROM games ORDER BY last_access LIMIT 2\G
```

```text
        table: games
         type: index
          key: ix_games_last_access
      key_len: 4
         rows: 2
        Extra: Using index
```

前者用 `ix_games_last_access` 做范围删除；后者是 **覆盖索引 + 天然有序**，
`ORDER BY last_access LIMIT n` 直接取索引前 n 条，没有 filesort（`Extra: Using index`）。

> 顺带暴露的一点：正是这条 `DELETE ... WHERE last_access < ?` 的 `type=range` 范围扫描，
> 在 REPEATABLE READ 下会加 next-key 锁，成为 §6.2 死锁的根因。

---

## 4. JSON 列在 MySQL 5.7 上的真实行为

`SQLAlchemy` 的 `JSON` 类型在 MySQL 上落到**原生 `json` 列**（不是 TEXT），实测：

```text
mysql> SELECT game_id, JSON_TYPE(wall) AS wall_type, JSON_LENGTH(wall) AS wall_len,
              JSON_VALID(wall) AS wall_valid, JSON_TYPE(hands->'$.east') AS east_type
       FROM games ORDER BY created_at DESC LIMIT 1;
+----------------------------------+-----------+----------+------------+-----------+
| game_id                          | wall_type | wall_len | wall_valid | east_type |
+----------------------------------+-----------+----------+------------+-----------+
| 892ad8f42118498c9b4197fc2d36b9f3 | ARRAY     |       82 |          1 | ARRAY     |
+----------------------------------+-----------+----------+------------+-----------+
```

三条要记牢的行为：

1. **数组顺序保留**：`wall` / 每家的手牌都是 JSON 数组，MySQL 严格保留数组元素顺序。
   集成用例逐个元素断言 `fetched.wall == state.wall`，牌墙顺序一字不差。
   → 摸牌/打牌依赖「从尾部弹出」，这一点在 MySQL 上和 SQLite 完全一致。
2. **对象 key 会被重排**：`{"east","south","west","north"}` 存进去，库里读出来是
   `{"east","west","north","south"}`（MySQL 按 key 长度 + 字典序重排，这是 JSON 二进制格式的规定）：

   ```text
   写入顺序：east, south, west, north
   库里内容：{"east": [...], "west": [...], "north": [...], "south": [...]}
   ```

   → 代码里从不当它是「有序字典」，只按 key 取值（`_row_to_state` 用 `.items()`），所以语义不变。
   但如果哪天有人写「按 key 顺序遍历输出四家」，SQLite 与 MySQL 就会给出不同顺序——这是个暗坑，记在 §8。
3. **原生类型带来的校验与函数**：`JSON_VALID()` / `JSON_LENGTH()` / `->` / `->>` 都能用，
   而 SQLite 那份实现只是把 JSON 存成 TEXT（无类型校验、无这些函数）。所以「同一行 SQL
   在两种库上都跑」的假设不成立——这也是 ADR 里「JSON 列不做牌级索引」的代价。

---

## 5. 行锁 `SELECT ... FOR UPDATE` 与 SQLite 的差异（实测）

代码里本来就有方言分支（`app/services/repository/sql.py:228`）：

```python
stmt = select(GameRow).where(GameRow.game_id == game_id)
if conn.dialect.name != "sqlite":
    stmt = stmt.with_for_update()      # MySQL / PostgreSQL：行锁串行化同一局
```

实测下来，同一段代码在两种库上的**失败形态**不同，但**最终状态一致**（这是关键）：

| | SQLite（aiosqlite） | MySQL 5.7（aiomysql） |
|---|---|---|
| 并发打牌 | 无行锁，靠 `version` CAS；两个事务都读到 version=0，后提交者 `WHERE version=0` 不命中 → **409 `GAME_CONFLICT`** | `FOR UPDATE` 把同一局串行化；输的一方等锁，锁释放后读到 `current_player` 已变成 `south` → **400 `NOT_YOUR_TURN`** |
| 冲突概率 | 高（每次并发都 CAS 冲突） | 基本为 0（409 很难出现） |
| 最终状态 | 牌河只多 1 张、牌墙 82、总牌数 136、version=1 | 完全相同 |

集成用例 `test_MySQL并发打同一局只有一次成功且状态不错乱` 打印的真实结果：一次成功，
一次 `AppError`（`NOT_YOUR_TURN`），随后查库 `discards["east"] == [该牌]`、`version == 1`、
`_total_tiles == 136`——**「一牌两打」没有发生**。

那 MySQL 上 CAS 409 是不是就永远走不到了？不是。CAS 那层仍然在，只是行锁让它很少被触发。
`test_MySQL上过期版本号的写回被CAS拦住并映射成409` 绕过行锁直接验证了兜底层：

```text
UPDATE games SET ... WHERE game_id = '...' AND version = 0   →  rowcount == 0
                                                             →  行未被覆盖（current_player 仍是 south）
```

仓库代码对这一情形的映射就是 `ConcurrentUpdateError` → HTTP **409 `GAME_CONFLICT`**。
也就是说：**MySQL 上「行锁负责不并发、CAS 负责不静默覆盖旧快照」，两层都实测有效**，
只是高频冲突场景下用户看到的是 400 而不是 409（面试官很可能追问这一点，见 §10）。

---

## 6. 实测发现的两个真问题（SQLite 上永远看不到）

这两个问题都在 `app/**` 里，**超出本工程的写权限**，所以本报告只做「定位 + 复现 + 修复建议」，
不改代码；是否开工单由 Lead 决定。两个问题都配了**能在 CI 里钉住它**的集成用例。

### 6.1 `Float` → 4 字节 `FLOAT`：epoch 秒被量化到万秒级

**证据 1：列类型**

```text
mysql> SELECT COLUMN_TYPE FROM information_schema.COLUMNS
       WHERE TABLE_SCHEMA='mahjong' AND TABLE_NAME='games' AND COLUMN_NAME='last_access';
float
```

SQLAlchemy 的 `Float` 在 MySQL 上映射成 4 字节单精度 `FLOAT`；SQLite 的 `REAL` 是 8 字节双精度。
`last_access` 存的是 `time.time()`，2026 年的值约 `1.79e9`。

**证据 2：float4 的量化步长把秒级差异全吃掉**

`1.79e9` 附近 float4 的 ULP = 128 秒。写进去 3 个相差 1 秒、37 秒的值，服务端只剩 1 个可区分值：

```text
mysql> SELECT COUNT(DISTINCT last_access) FROM games WHERE game_id IN (...);  -- 3 行，值差 1s / 37s
1
```

**证据 3：客户端读回的值更糟（6 位有效数字 → 万秒刻度）**

```text
原值              : 1793596399.9289362
FLOAT  (4 字节)   : 1793600000.0    误差 +3600.071 秒   ← 客户端实际拿到的值
DOUBLE (8 字节)   : 1793596399.9289362  误差 +0.000000000 秒
```

原因：MySQL 5.7 按 6 位有效数字输出 `FLOAT`（C 的 `FLT_DIG`），`pymysql` 文本协议把
`"1793600000"` 交给 Python。所以 `_row_to_state()` 读回的 `last_access` 最坏能差**约 1 小时**。

**后果（可复现）**：既有用例 `test_SQL实现遵守对局数上限与TTL清理` 会概率性失败。
它把 `last_access` 回拨 61 秒，期望 TTL(60s) 清理能删掉；但 float4 舍入误差 ±64 秒，
一旦向上舍入超过 +1 秒，库里存的值就`>= 阈值`，于是：

```text
回拨写入        : 1793596409.6577134
读回            : 1793600000.0     误差 +3590.342 秒
TTL 阈值(now-60): 1793596410.6612036
库里值 < 阈值？  False  →  不会被清理 ← 既有用例在这一步失败
```

实测频次：第一批 12 次单跑 **8 次失败**，第二批 8 次 **0 次失败**；
全量 18 个用例跑了 11 轮，其中 **4 轮 1 failed / 17 passed、7 轮 18 passed**。
**判定：概率性失败，根因是 float4 舍入方向，不是环境问题。**

**修复建议（app 代码，需 Lead 决策）**

| 方案 | 改动 | 说明 |
|---|---|---|
| A（最小） | `mapped_column(Double, ...)` | MySQL → `DOUBLE`，SQLite 仍是 `REAL`，双精度，一行代码 |
| B | `Float(precision=53)` | 效果同 A，SQLAlchemy 会在 MySQL 生成 `FLOAT(53)` = DOUBLE |
| C | 改存 `BIGINT` 毫秒 | 完全避开浮点；但 `_purge_expired` 的比较逻辑要跟着改 |
| D | 改存 `DATETIME` | 语义最正，可与 `created_at` 统一；TTL 改用 `NOW() - INTERVAL` |

配套建议：把该用例里的时间断言改成「回拨 300 秒」（远大于 128 秒量化步长），
这样即使在旧类型上也不会偶发失败——但那是**掩盖**，正确做法仍是改列类型。

### 6.2 并发建局触发 `1213 Deadlock`（间隙锁）

**复现**：10 个 `repo.create()` 并发（探针脚本 + 集成用例都复现了，8 并发时约 1/4 轮次触发）。
异常：

```text
sqlalchemy.exc.OperationalError: (pymysql.err.OperationalError)
(1213, 'Deadlock found when trying to get lock; try restarting transaction')
[SQL: DELETE FROM games WHERE games.last_access < %s]
```

**根因证据**：`SHOW ENGINE INNODB STATUS` 的 `LATEST DETECTED DEADLOCK`（原文节选）：

```text
*** (1) TRANSACTION:
TRANSACTION 2311, ACTIVE 0 sec starting index read
mysql tables in use 1, locked 1
LOCK WAIT 2 lock struct(s), heap size 1136, 1 row lock(s)
mysql thread id 275, ... updating
DELETE FROM games WHERE games.last_access < 1793589199.9208226e0
*** (1) WAITING FOR THIS LOCK TO BE GRANTED:
RECORD LOCKS space id 24 page no 4 n bits 176 index ix_games_last_access of table `mahjong`.`games`
 trx id 2311 lock_mode X waiting
Record lock, heap no 41 PHYSICAL RECORD: n_fields 2; compact format; info bits 32

*** (2) TRANSACTION:
TRANSACTION 2310, ACTIVE 0 sec inserting
mysql tables in use 1, locked 1
4 lock struct(s), heap size 1136, 11 row lock(s), undo log entries 1
mysql thread id 273, ... updating
INSERT INTO games (game_id, seed, wall, hands, discards, current_player, created_at, last_access, version)
VALUES ('01fc8cfa66b94976b53bf74b68c380ce', 5, ...)
*** (2) HOLDS THE LOCK(S):
RECORD LOCKS space id 24 page no 4 n bits 176 index ix_games_last_access of table `mahjong`.`games`
```

翻译成人话：`create()` 事务的第一句是 `_purge_expired()` 的
`DELETE FROM games WHERE last_access < ?`。在默认的 **REPEATABLE READ** 下，这个范围删除
会对 `ix_games_last_access` 加 **next-key 锁（范围 + 间隙）**；紧接着同一事务 `INSERT` 新行，
需要往这个索引里插记录（插入意向锁），而**别的事务**的 DELETE 还攥着那段间隙的锁 →
两个事务互相等对方持有的索引锁 → InnoDB 判定死锁，回滚其中一个（1213）。

**为什么 SQLite 上看不到**：SQLite 写是整库串行的，没有间隙锁，并发建局只是排队慢，不会死锁。
这就是「只在 SQLite 上验证过」会漏掉的典型问题。

**修复建议（app 代码，需 Lead 决策）**

| 方案 | 说明 | 代价 |
|---|---|---|
| A（推荐） | 把 `_purge_expired` / `_evict_overflow` **移出 `create` 的事务**，放到启动时的一次性任务或后台定时任务 | 语义不变，事务变短；需要一个小 scheduler |
| B | `create` 里清理失败就吞掉（`try/except` 只记日志）+ 对 1213 做**有限重试** | 改动最小，但治标 |
| C | 连接级 `SET SESSION TRANSACTION ISOLATION LEVEL READ COMMITTED`（去掉间隙锁） | 实测 4 轮 × 3/5/10 并发 0 死锁；但 RC 会改变其他事务的可见性语义，要评估 |
| D | `DELETE ... LIMIT n` + 单独提交的短事务 | 减少单次持锁范围 |

集成用例 `test_MySQL并发建局遇1213死锁重试后全部成功` 采用「应用层重试」证明语义正确
（10 个并发最终全部成功、game_id 唯一、每局都能读回 136 张牌），并在每次运行时打印重试次数：

```text
[MySQL实测] 10 并发建局共触发死锁重试 0 次     ← 运气好的一轮
（复现轮次里是 6 次重试 / 6 个 1213）
```

**注意**：这个用例允许重试，是**为了在偶发死锁的现状下保持绿色并留下证据**，
不代表问题已解决。真正的修复在 §6.2 的方案 A/B。

---

## 7. DSN 写法与「只改环境变量」的验证

```bash
# MySQL（本报告实测；password 为空时写成 root@host，有密码是 root:pass@host）
DATABASE_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong"
# 带密码的生产写法（密码不会进日志，mask_dsn 会换成 ***）
DATABASE_URL="mysql+aiomysql://app_user:******@10.0.0.12:3306/mahjong?charset=utf8mb4"

# 不设 = 内存模式（默认，零依赖）
# SQLite 本地验证
DATABASE_URL="sqlite+aiosqlite:///./games.db"
```

要点：

1. **驱动段必须是 `mysql+aiomysql`**，不是裸 `mysql://`（裸 `mysql://` 会去要 `MySQLdb`，
   即同步驱动，装不上就报错）。`aiomysql` 在 `pyproject.toml` 的 `[project.optional-dependencies].mysql` 里。
2. 代码**一行没改**：`Settings.database_url` → `create_engine_from_url()` → `init_models()` →
   `SqlGameRepository`，路由层与 schema 层完全无感知（这正是 ADR-0002 的「依赖倒置」红利，
   本次实测是它的第一次真实兑现）。
3. 启动日志里 DSN 会被 `mask_dsn` 掩码（实测 `mask_dsn("mysql+aiomysql://root:secret@.../mahjong")`
   → `mysql+aiomysql://root:***@.../mahjong`，见集成用例 #11）。
4. Windows 下 `--protocol=tcp` 要显式给（否则 mysql 客户端可能尝试命名管道或共享内存）；
   服务端 `bind-address=127.0.0.1` 足够，不需要开防火墙。

---

## 8. 已知限制 / 未覆盖

**没测的**

- **MySQL 8.0 / 5.6 / 8.4 没测**：只测了 5.7.44。8.0 的 `int` 不再带显示宽度、认证插件默认
  `caching_sha2_password`（`aiomysql` 需 0.2+，本机 0.3.2 应该没问题，但**未实测**），
  `FLOAT` 输出格式也可能不同（§6.1 的读回量化行为是 5.7 的 `FLT_DIG=6` 造成的）。
  集成用例里对「读回量化」那条断言加了 `server_version.startswith("5.7.")` 的门控，
  换 8.0 跑会自动只校验「存储层 float4 精度」，不会误报。
- **没测多实例部署**：两个 uvicorn 进程共享同一 MySQL 的并发行为未验证（跨进程锁与
  `asyncio.Lock` 的差异见 ADR 第 6 节，属于设计讨论，不属于本次实测范围）。
- **没测连接池打满**：`pool_size` / `max_overflow` 用默认值（5/10），没做压测；
  `max_games=100` 时最多 100 行，量级太小，压不出池耗尽。
- **没测长事务 / 锁等待超时**：`innodb_lock_wait_timeout` 默认 50s，没构造慢事务。
- **没测 TLS / 密码认证**：隔离实例 `--initialize-insecure`，root 无密码、走明文回环；
  生产 DSN 的 `?ssl=` 参数未验证。

**已知但未修（都不在本工程写权限内）**

- §6.1 `Float` 精度：建议 `Double` / `Float(precision=53)` / `BIGINT` / `DATETIME`，待 Lead 决策。
- §6.2 并发建局死锁：建议把 TTL 清理移出 `create` 事务或加 1213 重试，待 Lead 决策。
- **JSON 对象 key 被重排**：四家手牌的对象 key 顺序在 MySQL 与 SQLite 上不同。
  当前代码按 key 取值所以语义一致；但任何「按 key 顺序输出」的写法都会在两个库上分叉。
  建议在 `_row_to_state` 或 schema 层显式按 `SEATS` 排序，把顺序变成契约。
- `create_all` 建表、没有 Alembic 迁移（ADR 已记录）；MySQL 上加字段/改类型只能手工 `ALTER`。

**环境纪律**

- 全程没连 3306、没读它的数据、没改它的任何配置；收尾时已确认 3306（PID 7056）仍在正常监听。
- 隔离实例用 3307 + `.tmp/mysql-test/data`，`.tmp/` 在 `.gitignore`（第 46 行），不进仓库。

---

## 9. 如何再次启动 / 停止（收尾已关闭，确认干净）

**本报告写作结束后，3307 的 mysqld 已用 `mysqladmin shutdown` 正常关闭**，验证输出：

```text
$ "/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqladmin.exe" -h 127.0.0.1 -P 3307 -u root --protocol=tcp shutdown
（无输出 = 正常）
mysqld 日志：C:\...\mysqld.exe: Normal shutdown ... Shutdown complete

$ netstat -ano | grep -E ":3306|:3307"
  TCP    0.0.0.0:3306    0.0.0.0:0    LISTENING    7056     ← 只剩用户自己的 3306
$ tasklist | grep -i mysqld
mysqld.exe    7056 Services    0    2,096 K                    ← 3307 进程已退出
```

`datadir` 保留在 `.tmp/mysql-test/data`（已初始化好、含 `mahjong` / `mahjong_explain` 两个库），
所以**再次启动是秒级的**，不需要重新 `--initialize-insecure`：

```bash
cd /c/Users/SumMi/Documents/agent-learning/fastapi

# 启动（后台）
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqld.exe" \
  --defaults-file="C:/Users/SumMi/Documents/agent-learning/fastapi/.tmp/mysql-test/my.ini" --console &

# 等端口起来并验证
sleep 6
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysql.exe" -h 127.0.0.1 -P 3307 -u root --protocol=tcp -e "select version()"

# 跑集成测试
MYSQL_TEST_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong" \
  .venv/Scripts/python.exe -m pytest -o addopts="" -q tests/test_mysql_integration.py

# 跑既有 18 个持久化用例（MySQL 模式，不改测试文件）
PYTHONPATH="$PWD/.tmp/mysql-test" \
MYSQL_TEST_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong" \
  .venv/Scripts/python.exe -m pytest -o addopts="" -q tests/test_persistence.py -p mysql_shim_plugin

# 收尾：正常关闭，不要留后台进程
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqladmin.exe" -h 127.0.0.1 -P 3307 -u root --protocol=tcp shutdown
```

**想把一切推倒重来**（例如换字符集、换版本）：

```bash
"/c/Program Files/MySQL/MySQL Server 5.7/bin/mysqladmin.exe" -h 127.0.0.1 -P 3307 -u root --protocol=tcp shutdown
rm -rf .tmp/mysql-test/data          # 只删隔离实例的数据，与 3306 无关
# 然后从 §1.3 的初始化命令开始
```

---

## 10. 面试可以怎么讲（3-5 句，诚实口径）

> 「持久化这块我不只写了代码：本机装了 MySQL 5.7.44，我起了个 3307 的隔离实例（没动现有 3306
> 的库），把 SQLAlchemy 异步那条分支真的跑起来了——建表、建局、打牌、重启后按 game_id
> 还查得到，全部有测试和 curl 记录。换存储确实只改 `DATABASE_URL`，路由和规则代码一行没动。
> 跑的过程中还抓到两个只在 MySQL 上才会暴露的问题：一是 `Float` 存 epoch 时间戳在 MySQL 上
> 是 4 字节单精度、精度被量化到万秒级，TTL 逻辑会失真；二是建局事务里的清理 DELETE 会和并发
> INSERT 在同一个索引上形成间隙锁死锁，我拿 `SHOW ENGINE INNODB STATUS` 把死锁现场抓出来了。
> 这两个问题我按影响面给了修复选项（改 DOUBLE / 把清理移出事务或加 1213 重试），
> 但因为改动在别人的模块里，我没直接动，而是写成报告交给负责人决策。」

一句话精简版：**「SQLite 上绿不算绿，MySQL 上跑过才算；而且我跑出来的不是『能跑』，是两个真 bug 的现场。」**

面试官可能追问（自测清单，附本报告里的答案位置）：

1. **「为什么 SQLite 上测试全绿，MySQL 上会有问题？」**
   → 类型映射不同（`Float`→float4 vs REAL、`JSON`→原生 json vs TEXT）、锁模型不同
   （无行锁/无间隙锁 vs next-key lock）、无 SQLite 的 `:memory:` 单连接语义。见 §6、§5。
2. **「并发打牌你怎么保证不丢更新？MySQL 上为什么失败码是 400 不是 409？」**
   → 行锁 + 版本号 CAS 两层；MySQL 的 `FOR UPDATE` 先把同一局串行化，输的一方看到
   `current_player` 已变，直接 400 `NOT_YOUR_TURN`；409 只在「客户端拿旧快照写回」这类
   绕过行锁的路径出现，我用绕过 FOR UPDATE 的裸 UPDATE 验证了 CAS 仍然拦得住。见 §5。
3. **「EXPLAIN 看过吗？」**
   → 10 万行数据下，按主键查是 `type=const, rows=1`，CAS 更新是 `type=range, rows=1`，
   TTL 删除走 `ix_games_last_access` 范围扫描，LRU 淘汰是覆盖索引无 filesort。见 §3。
4. **「那你怎么保证线上不出事？」**（诚实版）
   → 目前 `create_all` 建表、没有迁移；连接池默认参数；这两个实测问题还没修。
   真上线前至少要：改 DOUBLE、清理移出事务、接 Alembic、按 `workers × 并发` 调池子。
5. **「为什么用 JSON 列不拆表？」**
   → 聚合根一次读写、无牌级查询；代价是不能按牌建索引、JSON 有序列化开销、
   MySQL 会重排对象 key（§4 实测）。真要做牌谱分析另建事件表。

---

## 附录：文件清单

**入库的（本工程唯一交付）**

| 路径 | 说明 |
|---|---|
| `tests/test_mysql_integration.py` | 11 个 MySQL 集成用例，`MYSQL_TEST_URL` 门控，默认 skip |
| `docs/mysql-实测报告.md` | 本文件 |

**临时证据（`.tmp/mysql-test/`，gitignore，不进仓库，可随时删）**

| 路径 | 用途 |
|---|---|
| `my.ini` | 隔离实例配置（3307 / utf8mb4 / 显式 basedir+datadir） |
| `data/` | 隔离实例 datadir（`--initialize-insecure`） |
| `smoke.py` | 冒烟：建表 / create / get / discard / 列类型 |
| `probe_deadlock.py` | 触发并发建局 1213 死锁，统计 2/3/5/10 并发 |
| `probe_isolation.py` | 默认 RR vs READ COMMITTED 的死锁对比 |
| `probe_innodb_status.py` | 抓 `SHOW ENGINE INNODB STATUS` 的 `LATEST DETECTED DEADLOCK` 原文 |
| `probe_float.py` | 实测 FLOAT / DOUBLE 精度差、TTL 阈值失效现场 |
| `mysql_shim_plugin.py` | pytest 插件：把既有 `test_persistence.py` 重定向到「每测试一个 MySQL 库」 |

**没动过的**：`app/**`、`pyproject.toml`、`README.md`、`docs/` 其他文件、`.env.example`、
`go-engine/`、3306 实例及其数据。未做任何 `git commit` / `push`。


---

## §10 修复与复测（工程 D 补充，2026-11-02）

> 本节为修复后**追加**：§1–§10（原先的报告内容，含 §6 的两个缺陷现场与 §9 的启停记录）
> 保持原样未改。改动只涉及三个文件：`app/services/repository/sql.py`（修复）、
> `tests/test_mysql_integration.py`（把「缺陷现场用例」改写成「修复后回归」）、
> 新增 `tests/test_sql_repository.py`（SQLite 侧回归，默认套件零依赖）。
> `app/api/**`、`app/main.py`、`app/services/repository/{base,store,redis}.py`、
> `pyproject.toml`、`README.md` 均未动。

### §10.1 缺陷 1：`Float` → `Double`（epoch 秒不再被量化）

**改动**（`GameRow`）

```python
# 前：mapped_column(Float, nullable=False, index=True)  → MySQL FLOAT（4 字节）
last_access: Mapped[float] = mapped_column(Double, nullable=False, index=True)
```

- MySQL 上生成 `double`（8 字节），SQLite 上仍是 8 字节 `REAL`，两种存储精度一致；
- `init_models()` docstring 补了一句：`create_all` **不会**修改已存在表的列类型，
  老库需要一次手工 `ALTER TABLE games MODIFY last_access DOUBLE NOT NULL;`（或接 Alembic）；
- 回归哨兵：`tests/test_sql_repository.py::test_SQLite的last_access列保留亚秒级精度`
  （SQLite 侧零依赖跑），以及 `tests/test_mysql_integration.py` 里改写后的
  `test_MySQL的last_access列是DOUBLE且精度与TTL边界可靠`（真 MySQL 上断言列类型 + 精度 + TTL 边界）。

**复测（3307 真 MySQL 5.7.44）**

```text
== D. 环境 ==
server_version=5.7.44  isolation=REPEATABLE-READ  last_access=double

轮次 | LRU 淘汰正确 | 回拨 61s 被清理 | 3 个戳读回 distinct | 最大读回误差(ms)
   1 |       True |           True |                  3 |        0.000000
   ...（12 轮全同）
  12 |       True |           True |                  3 |        0.000000
结论: 12/12 轮全过；最大读回误差 0.000000 ms（float4 时代约 3600000 ms）
```

对照 §6.1 的原始现场：修复前「3 个戳只读回 1 个可区分值、误差 +3600 秒、TTL 用例 12 次里 8 次失败」；
修复后 `distinct=3`、误差 `0.000000 ms`、12/12 轮稳定清理。

### §10.2 缺陷 2：建局死锁 → 维护动作独立事务 + 有限重试

**根因回顾**（§6.2）：`create()` 的事务开头是 `_purge_expired()` 的
`DELETE FROM games WHERE last_access < ?`，RR 下对 `ix_games_last_access` 加 next-key 锁；
同一事务随后 `INSERT` 需要插入意向锁 → 并发建局互相等锁 → 1213。

**改动**（`app/services/repository/sql.py`）

1. **结构上消环**：`create()` 拆成两步，各自独立事务、各自提交：
   - 维护事务：TTL 清理 + LRU 淘汰（`_maintenance`，可重入：每次从库里重算过期集合/最旧集合，
     并发/重复调用最多删不到，不会越删）；
   - 插入事务：只做一条 `INSERT`（`_insert_new_game`）。
   维护事务提交后锁即释放，插入事务开始时不再持有任何范围锁，并发建局之间不再形成锁环。
   `get()` / `discard()` 路径同样把 TTL 清理拆成独立事务（`_purge_expired_only`）。
2. **维护动作 best-effort**：清理/淘汰失败只记 `WARNING` 并降级，不让一次锁冲突把业务请求打成 500
   （下一次请求会再试，TTL 语义不变）。
3. **有限重试兜底**：新增 `RetryPolicy`（frozen dataclass）：
   - 只重试 MySQL **1213 死锁 / 1205 锁等待超时**（InnoDB 已回滚事务，重试安全）；
     业务异常（`ConcurrentUpdateError` 等 `AppError`）与其它 SQL 错误**立即抛出**；
   - 默认 `attempts=3`（最多重试 2 次），指数退避 `20ms → 40ms`、上限 `200ms`，叠 ±50% 抖动；
   - `create` / `get` / `discard` / `clear` 全部套用；维护动作用 `best_effort=True`；
   - 观测：`on_retry(operation, attempt, exc)` 回调 + `repo.retry_count` / `repo.retry_counts`
     （按操作分组），后续接 Prometheus 只需在回调里上报。
4. `discard` 的「行锁 + 版本号 CAS」语义不变，只是事务里不再夹带 `DELETE`。

**复测 1：原探针脚本（修复前 8 并发约 1/4 轮次触发 1213）**

```text
$ .venv/Scripts/python.exe .tmp/mysql-test/probe_deadlock.py
并发 2 第 1 轮: 成功 2 失败 0
并发 2 第 2 轮: 成功 2 失败 0
并发 2 第 3 轮: 成功 2 失败 0
并发 3 第 1 轮: 成功 3 失败 0
...（3 / 5 / 10 并发各 3 轮，全部 0 失败）
并发 10 第 3 轮: 成功 10 失败 0
```

**复测 2：20 协程并发建局 × 10 轮（`.tmp/mysql-test/verify_fix.py`）**

```text
== A. 并发建局 20 协程 × 10 轮 ==
轮次 | 成功 | 1213/1205 逃逸 | 内部重试次数 | 本轮重试事件(操作:第几次失败)
   1 |   20 |              0 |            0 | {}
   2 |   20 |              0 |            0 | {}
   ...（10 轮全同）
  10 |   20 |              0 |            0 | {}
合计: 建局 200 次，成功 200，异常逃逸 0（其中 1213/1205 0），内部重试 0 次
重试分布(操作, 第几次失败): {}
每轮重试次数: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0]
单轮最多重试 0 次 / 单操作上限 2 次（attempts=3）
```

解读：**重试次数为 0 不是「碰巧没触发」，而是结构修复后已经不存在可触发的锁环**——
原来「同事务先删后插」的场景消失了；`RetryPolicy` 是第二层保险。为了证明这层保险真的有效，
又做了下面这次「强制 1205」实验。

**复测 3：人为制造真实 1205，验证重试确实生效**（`.tmp/mysql-test/probe_retry_mysql.py`）

做法：本探针的 engine 用 `init_command` 把**会话级** `innodb_lock_wait_timeout` 调成 1 秒
（只影响本 engine 的连接，不动全局、不碰 3306），另一条连接对目标行 `SELECT ... FOR UPDATE`
并持有 2.6 秒。注意实测：MySQL 5.7 的锁等待超时按约 `2 × innodb_lock_wait_timeout`
的粒度触发（会话值 1s → 约 2.01s 才报 1205，pymysql 原始实验复现），所以行锁要持有 2.6s。

```text
== 阶段 A：维护事务（TTL 清理）撞外部行锁，锁持有 2.6s ==
调用耗时 2.61s；重试事件(操作, 第几次失败, 异常): [('maintenance', 1, 'OperationalError')]
累计重试 1 次；按操作: {'maintenance': 1}
打牌成功: wall_remaining=82 current_player=south version=1 总牌数=136
结论 A: 维护事务的 1205 被内部重试吸收，调用方看到的是正常成功（零异常逃逸）

== 阶段 B：打牌事务自身撞外部行锁，锁持有 2.6s ==
调用耗时 2.60s；重试事件(操作, 第几次失败, 异常): [('discard', 1, 'OperationalError')]
打牌成功: current_player=south version=1 wall_remaining=82
结论 B: 打牌事务的 1205 由 RetryPolicy 重试到成功，没有变成 500
```

**复测 4：用例层**

```text
# 真 MySQL 集成套件，连跑 10 轮
第 1 轮: 11 passed   ... 第 10 轮: 11 passed

# 既有 18 个持久化用例（不改编排，用 shim 重定向到 MySQL）
18 passed

# 原概率性失败的那条：TTL/LRU，连跑 12 轮（每轮一个全新 MySQL 库）
第 1 轮: 1 passed, 17 deselected   ... 第 12 轮: 1 passed, 17 deselected

# 默认套件（不设 MYSQL_TEST_URL，零依赖）
163 passed, 11 skipped
.venv/Scripts/python.exe -m ruff check app tests
All checks passed!
```

### §10.3 剩余限制 / 已知取舍（写在交付里，不藏着）

1. **老库没有迁移**：`create_all` 不改列类型，历史库必须手工
   `ALTER TABLE games MODIFY last_access DOUBLE NOT NULL;`；长期方案是接 Alembic（当前未做）。
2. **每请求多一次维护事务**：`get` / `discard` 也会先跑一次 TTL 清理（独立短事务、走索引范围删除）。
   收益是语义与内存实现一致（每次访问都清），代价是每请求多一次往返；若要压掉，可加节流
   （例如每 1s 最多清理一次）或改成后台定时任务（APScheduler / asyncio task）——未做，避免引入新组件。
3. **重试参数是经验值**：`attempts=3`、退避上限 200ms 适合当前单机演示；真实高并发写入应结合
   指标调整。重试耗尽后当前实现把 `OperationalError` 抛给全局 500 兜底；也可以选择映射成
   `503 + Retry-After`（需要改 errors 层，超出本次改动范围）。
4. **LRU 上限在并发下是「软上限」**：淘汰与插入拆成两个事务后，N 个并发建局瞬时最多可能超过
   `max_games` 1~N 行，下一次维护会收敛；单进程串行调用语义与内存实现一致（既有用例已覆盖）。
5. **SQLite 不受影响**：无行锁/无间隙锁，仍靠 `version` 乐观锁；1213/1205 只可能出现在 MySQL。
6. **未覆盖**：主从/网络分区、连接池参数调优、`innodb_lock_wait_timeout` 之外的 InnoDB 参数、
   长事务监控。

### §10.4 面试可以怎么讲（更新版）

> 「这两个问题不是我猜的，是拿本机隔离的 MySQL 5.7 实例实测出来的：一个是 `Float` 在 MySQL 上
> 是 4 字节单精度，存 epoch 时间戳会被量化到百秒级，TTL 用例 12 次里挂 8 次；另一个是建局事务里
> 先 DELETE 清理再 INSERT，在 RR 的间隙锁下并发建局会撞 1213。修法分别是把列换成 `Double`（MySQL
> 上是 8 字节 `DOUBLE`、SQLite 上仍是 REAL），以及把 TTL/LRU 维护动作拆成独立事务先提交、
> 让插入事务不再持有范围锁，再叠一层只对 1213/1205 生效的有限重试（3 次、指数退避 + 抖动）。
> 复测口径是 20 协程 × 10 轮建局 200 次 0 逃逸、0 重试（说明锁环已消失），另外用行锁硬造真实
> 1205 验证重试路径确实会把失败吸收成正常返回；TTL 用例连跑 12 轮全过，列类型和亚毫秒精度都有用例钉住。」
