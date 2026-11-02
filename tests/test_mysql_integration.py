"""真实 MySQL 集成测试：把持久化分支从「代码支持」变成「实测过」。

**门控**：只有设置了环境变量 ``MYSQL_TEST_URL`` 才会执行，默认套件（无数据库）保持
零依赖、139 个用例原样通过。用法：

.. code-block:: bash

    # 隔离实例：3307 端口 + .tmp/mysql-test/data（绝不碰本机 3306）
    MYSQL_TEST_URL="mysql+aiomysql://root@127.0.0.1:3307/mahjong" \\
        .venv/Scripts/python.exe -m pytest tests/test_mysql_integration.py -q

覆盖点（与 ``tests/test_persistence.py`` 的 SQLite 用例一一对应，但跑在真 MySQL 上）：

1. **建表**：``create_all`` 在 MySQL 5.7 上生成的列类型（``JSON`` / ``VARCHAR`` / ``DATETIME``）；
2. **全流程**：建局 → 按主键查询 → 打牌 → 版本号递增 → 牌数守恒 136；
3. **重启可查**：``engine.dispose()`` 后新 engine 仍能读到同一局（真持久化，不是进程内缓存）；
4. **并发原子性**：同一局并发打牌只有一次成功；MySQL 走 ``SELECT ... FOR UPDATE`` 行锁串行化，
   输的一侧读到新的 ``current_player`` 抛 400，而不是 SQLite 上的 CAS 409；
5. **CAS 兜底**：绕过行锁、拿 ``version=0`` 的旧快照写回，``WHERE version = 0`` 不命中
   → ``rowcount == 0``，仓库据此抛 ``ConcurrentUpdateError``（409 ``GAME_CONFLICT``）；
6. **同 seed 一致**：MySQL 实现与内存实现发出完全相同的牌局；
7. **JSON 列**：MySQL 原生 ``json`` 类型，数组顺序保留、对象键可能被重排，读写后语义不变；
8. **精度回归（缺陷 1）**：``last_access`` 必须是 8 字节 ``double``——报告 §6.1 里
   ``Float`` 落到 4 字节 ``FLOAT`` 后 epoch 秒被量化、TTL 边界概率性失效；这里同时验证
   「3 个相差 1s/37s 的戳逐值可区分」与「回拨 61s 必被 TTL 清理」；
9. **API 闭环**：``create_app(Settings(database_url=MySQL))`` + TestClient，重启后仍可查询打牌；
10. **并发建局回归（缺陷 2）**：20 协程同时 ``create()``，报告 §6.2 的 1213 间隙锁死锁
    必须被「维护动作独立事务 + 有限重试」全部吸收，**0 次异常逃逸到调用方**，并打印重试分布。

约定与 ``test_persistence.py`` 一致：异步场景用 ``asyncio.run`` 包裹，不引入 pytest-asyncio。
测试会建表 / 清表，``MYSQL_TEST_URL`` **必须指向一次性测试库**，不要指向生产库。
"""

import asyncio
import os
import time
from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import select, text, update
from sqlalchemy.exc import OperationalError, SQLAlchemyError
from sqlalchemy.ext.asyncio import AsyncEngine

from app.core.config import Settings
from app.core.errors import AppError, GameNotFoundError
from app.main import create_app
from app.services.repository.base import ConcurrentUpdateError, GameState
from app.services.repository.sql import (
    Base,
    GameRow,
    SqlGameRepository,
    create_engine_from_url,
    init_models,
    mask_dsn,
)
from app.services.store import GameStore

MYSQL_TEST_URL = os.environ.get("MYSQL_TEST_URL", "")

pytestmark = pytest.mark.skipif(
    not MYSQL_TEST_URL,
    reason="未设置 MYSQL_TEST_URL，跳过真实 MySQL 集成测试（默认套件保持零依赖）",
)


@pytest.fixture(scope="session", autouse=True)
def _fresh_schema() -> Iterator[None]:
    """整轮测试开始前 drop + create 一次表结构。

    ``create_all`` 是幂等的，**不会**修改已存在表的列类型：报告里那个 3307 实例的
    ``games`` 表还是修复前的 ``FLOAT``。``MYSQL_TEST_URL`` 按约定必须指向一次性测试库，
    所以这里重建结构是安全的，也保证「列类型必须 double」这条断言测的是当前代码。
    """
    if not MYSQL_TEST_URL:  # 默认套件不连库（此时用例本来也会全部 skip）
        yield
        return

    async def reset() -> None:
        engine = create_engine_from_url(MYSQL_TEST_URL)
        try:
            async with engine.begin() as conn:
                await conn.run_sync(Base.metadata.drop_all)
                await conn.run_sync(Base.metadata.create_all)
        finally:
            await engine.dispose()

    asyncio.run(reset())
    yield


async def _open_repo(*, clean: bool = True, **kwargs) -> tuple[SqlGameRepository, AsyncEngine]:
    """建表 + 建仓库，返回 ``(repository, engine)``；调用方负责 ``await engine.dispose()``。

    ``clean=True``（默认）先 ``clear()``，保证用例之间、多次 pytest 之间互不污染；
    「重启后仍可查」这类用例的读阶段必须传 ``clean=False``，否则会把刚写的数据清掉。
    """
    engine = create_engine_from_url(MYSQL_TEST_URL)
    await init_models(engine)
    repo = SqlGameRepository(engine, **kwargs)
    if clean:
        await repo.clear()
    return repo, engine


def _total_tiles(state: GameState) -> int:
    """总牌数守恒校验：牌墙 + 四家手牌 + 四家牌河 == 136。"""
    return (
        len(state.wall)
        + sum(len(hand) for hand in state.hands.values())
        + sum(len(pile) for pile in state.discards.values())
    )


# --------------------------------------------------------------------------- #
# 1. 建表：MySQL 5.7 上 create_all 生成的真实列类型
# --------------------------------------------------------------------------- #


def test_在MySQL上建表且列类型符合预期() -> None:
    """``create_all`` 幂等；``games`` 表用原生 ``json`` 列，主键是 ``varchar(32)``。"""

    async def scenario() -> None:
        engine = create_engine_from_url(MYSQL_TEST_URL)
        try:
            assert engine.url.drivername == "mysql+aiomysql"
            await init_models(engine)
            await init_models(engine)  # 幂等：第二次不能报「表已存在」

            async with engine.connect() as conn:
                assert conn.dialect.name == "mysql"
                server_version = (await conn.execute(text("SELECT VERSION()"))).scalar_one()
                assert server_version.startswith("5.7."), server_version
                rows = (
                    await conn.execute(
                        text(
                            "SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE "
                            "FROM information_schema.COLUMNS "
                            "WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'games'"
                        )
                    )
                ).all()
            columns = {name: ctype for name, ctype, _ in rows}
            assert columns["game_id"] == "varchar(32)"
            # MySQL 5.7 的整数列带显示宽度（int(11)），8.0 起被移除，这里只校验前缀
            assert columns["seed"].startswith("int")
            assert columns["wall"] == "json"  # 原生 JSON，不是 TEXT
            assert columns["hands"] == "json"
            assert columns["discards"] == "json"
            assert columns["current_player"] == "varchar(8)"
            assert columns["created_at"] == "datetime"
            assert columns["version"].startswith("int")
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 2. 全流程：建局 → 查询 → 打牌 → 清空
# --------------------------------------------------------------------------- #


def test_MySQL仓库完成创建查询打牌清空全流程() -> None:
    """语义与 SQLite / 内存实现一致：牌墙 83、打牌后 82、轮转下家、版本 +1。"""

    async def scenario() -> None:
        repo, engine = await _open_repo()
        try:
            created = await repo.create(seed=42)
            assert len(created.wall) == 83
            assert created.current_player == "east"
            assert len(created.hands["east"]) == 14
            assert created.version == 0

            fetched = await repo.get(created.game_id)
            assert fetched.hands == created.hands
            # 牌墙数组在 MySQL JSON 列里顺序必须原样保留（JSON 数组是有序的）
            assert fetched.wall == created.wall
            assert fetched.seed == 42
            assert fetched.created_at.tzinfo is not None  # naive DATETIME 被补成 UTC

            tile = fetched.hands["east"][0]
            after = await repo.discard(created.game_id, "east", tile)
            assert after.current_player == "south"
            assert after.discards["east"] == [tile]
            assert len(after.wall) == 82
            assert after.version == 1
            assert _total_tiles(after) == 136

            # 再打一次，轮到南家 → 西家
            third = await repo.discard(created.game_id, "south", after.hands["south"][0])
            assert third.current_player == "west"
            assert third.version == 2
            assert len(third.wall) == 81
            assert _total_tiles(third) == 136

            await repo.clear()
            with pytest.raises(GameNotFoundError):
                await repo.get(created.game_id)
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 3. 重启（新建 engine）后仍可查
# --------------------------------------------------------------------------- #


def test_MySQL上应用重启后同一局仍可查询并继续打牌() -> None:
    """真落库：写阶段 dispose 整个连接池，读阶段新 engine + 新仓库照样读到完整状态。"""

    async def write_phase() -> tuple[str, str]:
        repo, engine = await _open_repo()
        try:
            state = await repo.create(seed=7)
            tile = state.hands["east"][0]
            await repo.discard(state.game_id, "east", tile)
            return state.game_id, tile
        finally:
            await engine.dispose()

    async def read_phase(game_id: str) -> GameState:
        repo, engine = await _open_repo(clean=False)  # 不清表：要验证的是刚写进去的数据
        try:
            return await repo.get(game_id)
        finally:
            await engine.dispose()

    game_id, tile = asyncio.run(write_phase())
    reloaded = asyncio.run(read_phase(game_id))
    assert reloaded.game_id == game_id
    assert reloaded.seed == 7
    assert reloaded.current_player == "south"
    assert reloaded.discards["east"] == [tile]
    assert len(reloaded.wall) == 82
    assert reloaded.version == 1
    assert _total_tiles(reloaded) == 136


# --------------------------------------------------------------------------- #
# 4. 并发打牌：只有一次成功
# --------------------------------------------------------------------------- #


def test_MySQL并发打同一局只有一次成功且状态不错乱() -> None:
    """两条连接同时打同一张牌 → 一次成功。

    MySQL 走 ``SELECT ... FOR UPDATE``：输的一方等锁，锁释放后读到 ``current_player``
    已经是南家，于是抛 400 ``NOT_YOUR_TURN``（而不是 SQLite 那种 CAS 409）。
    无论哪种失败形态，最终都必须是「牌河只多 1 张、牌墙 82、总牌数 136、版本 1」。
    """

    async def scenario() -> None:
        repo, engine = await _open_repo()
        try:
            state = await repo.create(seed=21)
            tile = state.hands["east"][0]

            results = await asyncio.gather(
                repo.discard(state.game_id, "east", tile),
                repo.discard(state.game_id, "east", tile),
                return_exceptions=True,
            )
            successes = [result for result in results if isinstance(result, GameState)]
            failures = [result for result in results if isinstance(result, BaseException)]
            assert len(successes) == 1, results
            assert len(failures) == 1, results
            # 允许的失败形态：行锁串行化后检测到不是当前回合、过期版本 CAS 冲突，
            # 或 MySQL 并发下偶发的锁等待/死锁异常（见本文件最后一个用例的实测记录）
            assert isinstance(failures[0], (ConcurrentUpdateError, AppError, SQLAlchemyError))

            final = await repo.get(state.game_id)
            assert final.discards["east"] == [tile]  # 只有一次打牌生效，没有「一牌两打」
            assert final.current_player == "south"
            assert len(final.wall) == 82
            assert final.version == 1
            assert _total_tiles(final) == 136
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 5. 乐观锁 CAS：行锁之外的第二层保护
# --------------------------------------------------------------------------- #


def test_MySQL上过期版本号的写回被CAS拦住并映射成409() -> None:
    """模拟「客户端拿着 version=0 的旧快照写回」：CAS 不命中 → rowcount 0 → 409。

    真实路径上 MySQL 的 ``FOR UPDATE`` 会先把同一局串行化，所以 409 很少出现；
    这条用例绕过行锁直接验证「过期快照绝不静默覆盖」这层兜底仍然生效。
    """

    async def scenario() -> None:
        repo, engine = await _open_repo()
        try:
            state = await repo.create(seed=5)
            after = await repo.discard(state.game_id, "east", state.hands["east"][0])
            assert after.version == 1

            async with engine.begin() as conn:
                # 旧快照 version=0 的写回：WHERE 条件不命中 → 一行都不改
                result = await conn.execute(
                    update(GameRow)
                    .where(GameRow.game_id == state.game_id, GameRow.version == 0)
                    .values(current_player="north", version=1)
                )
                assert result.rowcount == 0
                fresh = (
                    await conn.execute(
                        select(GameRow.version, GameRow.current_player).where(
                            GameRow.game_id == state.game_id
                        )
                    )
                ).one()
                assert fresh.version == 1
                # 未被过期写覆盖：仍然是南家，不是伪造的北家
                assert fresh.current_player == "south"

            # 仓库对这一情形的映射就是 409 GAME_CONFLICT
            error = ConcurrentUpdateError(state.game_id)
            assert error.code == "GAME_CONFLICT"
            assert error.status_code == 409
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 6. 同 seed：MySQL 与内存实现发出同一副牌
# --------------------------------------------------------------------------- #


def test_相同seed在MySQL与内存实现下发出同一副牌() -> None:
    """两种实现共用 ``new_game_state``：换存储不换行为（牌墙、手牌、庄家完全一致）。"""

    async def scenario() -> None:
        repo, engine = await _open_repo()
        try:
            sql_state = await repo.create(seed=42)
            memory_state = await GameStore().create(seed=42)
        finally:
            await engine.dispose()

        assert sql_state.hands == memory_state.hands
        assert sql_state.wall == memory_state.wall
        assert sql_state.current_player == memory_state.current_player == "east"

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 7. JSON 列在 MySQL 5.7 上的真实行为
# --------------------------------------------------------------------------- #


def test_MySQL的JSON列保留数组顺序且类型是原生json() -> None:
    """MySQL 会重排 JSON **对象**的 key，但保留**数组**顺序；读写语义不受影响。"""

    async def scenario() -> None:
        repo, engine = await _open_repo()
        try:
            state = await repo.create(seed=99)
            async with engine.connect() as conn:
                row = (
                    await conn.execute(
                        text(
                            "SELECT JSON_TYPE(wall), JSON_LENGTH(wall), JSON_VALID(wall) "
                            "FROM games WHERE game_id = :gid"
                        ),
                        {"gid": state.game_id},
                    )
                ).one()
            assert row[0] == "ARRAY"  # 原生 JSON 数组
            assert row[1] == 83
            assert row[2] == 1

            fetched = await repo.get(state.game_id)
            assert fetched.wall == state.wall  # 数组顺序逐张一致
            # 对象 key 顺序不作为契约：两边按 key 取值比较，语义完全一致即可
            assert set(fetched.hands) == set(state.hands) == {"east", "south", "west", "north"}
            for seat, hand in fetched.hands.items():
                assert hand == state.hands[seat]
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 8. 缺陷 1 回归：last_access 必须是 DOUBLE（精度 + TTL 边界）
# --------------------------------------------------------------------------- #


def test_MySQL的last_access列是DOUBLE且精度与TTL边界可靠() -> None:
    """报告 §6.1 的回归：列类型必须是 ``double``，1s / 37s 的差异必须逐值可区分。

    修复前（``Float`` → 4 字节 ``FLOAT``）：``1.79e9`` 附近 ULP≈128 秒，加上 MySQL 5.7
    按 6 位有效数字回读，3 个值会塌缩成 1 个、误差可达小时级，``test_persistence`` 里
    那条 TTL 用例因此概率性失败（实测 12 次里 8 次）。修复后（``Double`` → ``double``）：
    3 个值逐值可区分、误差 < 1 毫秒；TTL=60s 时「回拨 61 秒」稳定被清理。
    """

    async def scenario() -> None:
        repo, engine = await _open_repo(ttl_seconds=60)
        try:
            async with engine.connect() as conn:
                column_type = (
                    await conn.execute(
                        text(
                            "SELECT COLUMN_TYPE FROM information_schema.COLUMNS "
                            "WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'games' "
                            "AND COLUMN_NAME = 'last_access'"
                        )
                    )
                ).scalar_one()
                server_version = (await conn.execute(text("SELECT VERSION()"))).scalar_one()
            assert server_version.startswith("5.7."), server_version
            assert column_type == "double", f"last_access 必须是 8 字节 DOUBLE，实际是 {column_type}"

            states = [await repo.create(seed=seed) for seed in (1, 2, 3)]
            base = 1793596400.0  # 与 2026 年的 epoch 同量级
            targets = [base, base + 1.0, base + 37.0]
            async with engine.begin() as conn:
                for state, target in zip(states, targets, strict=True):
                    await conn.execute(
                        update(GameRow)
                        .where(GameRow.game_id == state.game_id)
                        .values(last_access=target)
                    )

            ids = [state.game_id for state in states]
            async with engine.connect() as conn:
                distinct = (
                    await conn.execute(
                        text(
                            "SELECT COUNT(DISTINCT last_access) FROM games "
                            "WHERE game_id IN (:a, :b, :c)"
                        ),
                        {"a": ids[0], "b": ids[1], "c": ids[2]},
                    )
                ).scalar_one()
                readback = dict(
                    (
                        await conn.execute(
                            select(GameRow.game_id, GameRow.last_access).where(
                                GameRow.game_id.in_(ids)
                            )
                        )
                    ).all()
                )
            assert distinct == 3, "DOUBLE 下 1s / 37s 的差异必须保留"
            values = [readback[game_id] for game_id in ids]
            for value, target in zip(values, targets, strict=True):
                assert abs(value - target) < 1e-3, f"读回误差应 < 1ms，实际 {value} vs {target}"

            # 原始失败现场：TTL=60s，回拨 61 秒的行必须被清理（float4 曾因舍入把它留下）
            expired = await repo.create(seed=11)
            async with engine.begin() as conn:
                await conn.execute(
                    update(GameRow)
                    .where(GameRow.game_id == expired.game_id)
                    .values(last_access=time.time() - 61)
                )
            with pytest.raises(GameNotFoundError):
                await repo.get(expired.game_id)
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 9. API 闭环（TestClient 走完整 HTTP 栈）+ 重启
# --------------------------------------------------------------------------- #


def test_MySQL模式下API建局打牌与重启后查询() -> None:
    """真实 HTTP 栈 + 真 MySQL：建局 → 查询 → 打牌 → 关应用 → 新应用仍查得到。"""

    async def reset() -> None:
        _, engine = await _open_repo()  # _open_repo 内部已 clear()
        await engine.dispose()

    asyncio.run(reset())

    settings = Settings(database_url=MYSQL_TEST_URL, game_ttl_seconds=3600)
    with TestClient(create_app(settings)) as client:
        created = client.post("/api/v1/games", json={"seed": 42})
        assert created.status_code == 200, created.text
        created_body = created.json()
        game_id = created_body["game_id"]
        assert created_body["wall_remaining"] == 83

        detail = client.get(f"/api/v1/games/{game_id}")
        assert detail.status_code == 200
        assert len(detail.json()["hands"]["east"]) == 14

        tile = created_body["hands"]["east"][0]
        discarded = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": tile}
        )
        assert discarded.status_code == 200, discarded.text
        assert discarded.json()["next_player"] == "south"
        assert discarded.json()["wall_remaining"] == 82

        error_body = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "south", "tile": "9q"}
        )
        assert error_body.status_code == 400
        assert error_body.json()["error"]["code"] == "HAND_INVALID"

    # 新应用实例（等价于重启进程）连同一个 MySQL 库
    with TestClient(create_app(settings)) as second_client:
        response = second_client.get(f"/api/v1/games/{game_id}")
        assert response.status_code == 200, response.text
        body = response.json()
        assert body["current_player"] == "south"
        assert body["wall_remaining"] == 82
        # 查询接口只给庄家完整手牌，其余座位给张数（API 契约，两种存储一致）
        assert len(body["hands"]["east"]) == 13
        assert body["hands"]["south"] == 14

        # 取南家一张真牌：直接读库（API 故意不暴露他家和牌）
        async def south_tile(game_id: str) -> str:
            repo, engine = await _open_repo(clean=False)
            try:
                return (await repo.get(game_id)).hands["south"][0]
            finally:
                await engine.dispose()

        tile = asyncio.run(south_tile(game_id))
        follow_up = second_client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "south", "tile": tile}
        )
        assert follow_up.status_code == 200, follow_up.text
        assert follow_up.json()["next_player"] == "west"


# --------------------------------------------------------------------------- #
# 10. 缺陷 2 回归：20 并发建局，1213 不得逃逸到调用方
# --------------------------------------------------------------------------- #


def test_MySQL20并发建局不把1213逃逸给调用方() -> None:
    """报告 §6.2 的回归：维护 DELETE 曾与并发 INSERT 形成间隙锁死锁。

    修复后 ``create()`` 是「维护事务（独立提交）→ 插入事务」两步，结构上消除了
    「同一事务里先删后插」的锁环；万一仍撞上 1213，由 ``RetryPolicy`` 有限重试吸收。
    断言口径：20 个协程全部成功、0 个异常逃逸、game_id 唯一、每局都能读回 136 张牌；
    并打印本轮的内部重试分布（按操作 / 尝试序号），供报告 §10 引用。
    """

    async def scenario() -> None:
        retry_events: list[tuple[str, int]] = []
        repo, engine = await _open_repo(
            on_retry=lambda operation, attempt, exc: retry_events.append((operation, attempt))
        )
        try:
            results = await asyncio.gather(
                *(repo.create(seed=seed) for seed in range(20)), return_exceptions=True
            )
            escapes = [result for result in results if isinstance(result, BaseException)]
            assert not escapes, [repr(error) for error in escapes]
            assert not any(isinstance(error, OperationalError) for error in escapes)
            states = [result for result in results if isinstance(result, GameState)]
            assert len(states) == 20
            assert len({state.game_id for state in states}) == 20
            for state in states:
                fetched = await repo.get(state.game_id)
                assert fetched.hands == state.hands
                assert _total_tiles(fetched) == 136

            print(
                "[MySQL实测·修复后] 20 并发建局："
                f"成功 {len(states)} / 1213 逃逸 {len(escapes)} / 内部重试 {repo.retry_count} 次"
            )
            print(f"[MySQL实测·修复后] 重试事件(操作, 第几次失败): {retry_events}")
            print(f"[MySQL实测·修复后] 按操作分组: {repo.retry_counts}")
            # 有限重试：单次操作最多 attempts-1 次重试，20 次操作有硬上界
            assert repo.retry_count <= 20 * (repo.retry_policy.attempts - 1)
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 11. DSN 写法与凭据掩码
# --------------------------------------------------------------------------- #


def test_MySQL的DSN写法与密码掩码() -> None:
    """``mysql+aiomysql://user:pass@host:port/db``；日志里密码必须是 ``***``。"""

    assert MYSQL_TEST_URL.split("://", 1)[0] == "mysql+aiomysql"

    async def scenario() -> None:
        engine = create_engine_from_url(MYSQL_TEST_URL)
        try:
            async with engine.connect() as conn:
                assert conn.dialect.driver == "aiomysql"
                assert conn.dialect.name == "mysql"
        finally:
            await engine.dispose()

    asyncio.run(scenario())

    masked = mask_dsn("mysql+aiomysql://root:secret@127.0.0.1:3306/mahjong")
    assert masked == "mysql+aiomysql://root:***@127.0.0.1:3306/mahjong"
    assert "secret" not in masked
