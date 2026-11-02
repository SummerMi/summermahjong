"""SQLite 侧回归测试：把 MySQL 实测暴露的两个缺陷钉在「不依赖数据库服务」的层面。

MySQL 上的原始证据与复测见 ``docs/mysql-实测报告.md``（§6 问题现场、§10 修复与复测）。
本文件保证的是：

1. **精度**：``last_access`` 列必须是 8 字节双精度（SQLite 上是 ``REAL``）——这样
   epoch 秒的亚秒级差异不会丢，TTL 边界与 LRU 排序可靠（缺陷 1 的根因是 MySQL 上
   ``Float`` 落成 4 字节 ``FLOAT``，见报告 §6.1）。
2. **重试策略**：只重试 1213 死锁 / 1205 锁等待超时；次数有限、退避有上限且带抖动；
   业务异常与其它 SQL 错误一律立即抛出（缺陷 2 的兜底，报告 §6.2）。
3. **事务拆分**：TTL/LRU 维护动作走独立事务且 best-effort（失败只降级不拖垮请求），
   插入/打牌路径按策略重试——用「前 N 次 ``begin()`` 抛 1213」的 engine 代理验证接缝。

为什么放在 SQLite 侧：默认套件不启动任何数据库服务也能跑，CI 里零成本守住回归；
真正的 MySQL 行为由 ``tests/test_mysql_integration.py``（``MYSQL_TEST_URL`` 门控）覆盖。
"""

import asyncio
import time
from pathlib import Path
from typing import Any

import pytest
from sqlalchemy import select, update
from sqlalchemy.exc import OperationalError
from sqlalchemy.ext.asyncio import AsyncEngine

from app.core.errors import GameNotFoundError
from app.services.repository.base import ConcurrentUpdateError
from app.services.repository.sql import (
    GameRow,
    RetryPolicy,
    SqlGameRepository,
    create_engine_from_url,
    init_models,
)


def _sqlite_url(tmp_path: Path, name: str = "games.db") -> str:
    """拼一个 SQLite 文件库 DSN（posix 路径，避免 Windows 反斜杠被当成转义）。"""
    return f"sqlite+aiosqlite:///{(tmp_path / name).as_posix()}"


def _operational_error(code: int, message: str = "fake driver error") -> OperationalError:
    """构造与 pymysql 同形的 ``OperationalError``（``orig.args[0]`` 是 MySQL 错误码）。"""
    orig = Exception(code, message)
    return OperationalError("DELETE FROM games WHERE last_access < %s", {}, orig)


async def _open_repo(database_url: str, **kwargs: Any) -> tuple[SqlGameRepository, AsyncEngine]:
    """建表 + 建仓库；调用方负责 ``await engine.dispose()``。"""
    engine = create_engine_from_url(database_url)
    await init_models(engine)
    return SqlGameRepository(engine, **kwargs), engine


# --------------------------------------------------------------------------- #
# 1. RetryPolicy：只重试死锁 / 锁等待超时，且次数有限、退避有上限
# --------------------------------------------------------------------------- #


def test_只把1213死锁与1205锁等待超时视为可重试() -> None:
    """其它 MySQL 错误码、业务异常、以及文本里恰好含 1213 的普通异常都不重试。"""
    assert RetryPolicy.is_retryable(_operational_error(1213))
    assert RetryPolicy.is_retryable(_operational_error(1205))
    assert not RetryPolicy.is_retryable(_operational_error(1062, "Duplicate entry"))
    assert not RetryPolicy.is_retryable(ValueError("boom"))
    assert not RetryPolicy.is_retryable(ConcurrentUpdateError("game-1"))


def test_退避是指数增长且有上限和抖动() -> None:
    """退避 = min(base × 2^(n-1), max) × U(1±jitter)：绝不无限增长、绝不为负。"""
    policy = RetryPolicy(attempts=3, base_delay=0.02, max_delay=0.2, jitter=0.5)
    assert policy.attempts == 3  # 默认就是「最多重试 2 次」，不会无限重试

    delays = [policy.backoff(attempt) for attempt in range(1, 20)]
    assert all(delay >= 0.0 for delay in delays)
    assert all(delay <= 0.2 * 1.5 + 1e-12 for delay in delays)  # 上限 × 抖动系数
    # 前几次还没到上限：0.02 / 0.04 / 0.08，各自最多 ×1.5
    assert policy.backoff(1) <= 0.03 + 1e-12
    assert policy.backoff(2) <= 0.06 + 1e-12
    assert policy.backoff(3) <= 0.12 + 1e-12
    # 到了上限后不再增长（指数退避被 max_delay 截断）
    assert max(delays[5:]) <= 0.3 + 1e-12


def test_重试后成功并回调观测钩子() -> None:
    """第 1、2 次尝试抛 1213 → 第 3 次成功；钩子收到 2 次重试事件。"""
    policy = RetryPolicy(attempts=3, base_delay=0.0, max_delay=0.0)
    calls: list[int] = []
    events: list[tuple[str, int]] = []
    remaining = {"failures": 2}

    async def flaky() -> str:
        calls.append(1)
        if remaining["failures"] > 0:
            remaining["failures"] -= 1
            raise _operational_error(1213)
        return "ok"

    result = asyncio.run(
        policy.run(
            "create",
            flaky,
            on_retry=lambda operation, attempt, exc: events.append((operation, attempt)),
        )
    )
    assert result == "ok"
    assert len(calls) == 3
    assert events == [("create", 1), ("create", 2)]


def test_重试次数用尽后抛出而不是无限重试() -> None:
    """一直 1213 → 尝试 ``attempts`` 次后把原始异常抛回调用方。"""
    policy = RetryPolicy(attempts=2, base_delay=0.0, max_delay=0.0)
    calls: list[int] = []

    async def always_deadlock() -> None:
        calls.append(1)
        raise _operational_error(1213)

    with pytest.raises(OperationalError):
        asyncio.run(policy.run("discard", always_deadlock))
    assert len(calls) == 2


def test_业务异常和不可重试的SQL错误立即抛出() -> None:
    """``ConcurrentUpdateError``（409 业务冲突）与重复键错误都不该被重试。"""
    policy = RetryPolicy(attempts=3, base_delay=0.0, max_delay=0.0)

    business_calls: list[int] = []

    async def business_error() -> None:
        business_calls.append(1)
        raise ConcurrentUpdateError("game-1")

    with pytest.raises(ConcurrentUpdateError):
        asyncio.run(policy.run("discard", business_error))
    assert len(business_calls) == 1

    sql_calls: list[int] = []

    async def duplicate_key() -> None:
        sql_calls.append(1)
        raise _operational_error(1062, "Duplicate entry")

    with pytest.raises(OperationalError):
        asyncio.run(policy.run("create", duplicate_key))
    assert len(sql_calls) == 1


def test_best_effort维护动作失败只降级不抛() -> None:
    """TTL/LRU 维护失败不应让业务请求 500：重试耗尽后返回 ``None``。"""
    policy = RetryPolicy(attempts=2, base_delay=0.0, max_delay=0.0)

    async def always_deadlock() -> None:
        raise _operational_error(1213)

    assert asyncio.run(policy.run("maintenance", always_deadlock, best_effort=True)) is None

    async def not_retryable() -> None:
        raise _operational_error(1062, "Duplicate entry")

    assert asyncio.run(policy.run("maintenance", not_retryable, best_effort=True)) is None


# --------------------------------------------------------------------------- #
# 2. 精度回归：last_access 必须保留 epoch 秒的亚秒级差异
# --------------------------------------------------------------------------- #


def test_SQLite的last_access列保留亚秒级精度(tmp_path: Path) -> None:
    """写入 3 个相差 1 秒 / 37 秒的 epoch 时间戳，读回必须逐值可区分且误差 < 1 毫秒。

    MySQL 上如果列退化成 4 字节 ``FLOAT``，这里会塌缩成 1 个值（报告 §6.1 实测）；
    ``Double`` 在 SQLite 上是 8 字节 ``REAL``，所以这条用例就是缺陷 1 的 SQLite 侧哨兵。
    """

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            states = [await repo.create(seed=seed) for seed in (1, 2, 3)]
            base = 1793596400.0
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
                readback = dict(
                    (
                        await conn.execute(
                            select(GameRow.game_id, GameRow.last_access).where(
                                GameRow.game_id.in_(ids)
                            )
                        )
                    ).all()
                )
            values = [readback[game_id] for game_id in ids]
            assert len(set(values)) == 3, "亚秒级差异被量化掉了：列类型可能退化成 4 字节 FLOAT"
            for value, target in zip(values, targets, strict=True):
                assert abs(value - target) < 1e-3
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_TTL边界依赖精度且清理语义与内存一致(tmp_path: Path) -> None:
    """``last_access`` 回拨 61 秒（TTL=60）必须被清理——MySQL 上曾在这一步概率性失败。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path), max_games=10, ttl_seconds=60)
        try:
            expired = await repo.create(seed=1)
            alive = await repo.create(seed=2)
            async with engine.begin() as conn:
                await conn.execute(
                    update(GameRow)
                    .where(GameRow.game_id == expired.game_id)
                    .values(last_access=time.time() - 61)
                )
            with pytest.raises(GameNotFoundError):
                await repo.get(expired.game_id)
            assert (await repo.get(alive.game_id)).game_id == alive.game_id
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 3. 事务拆分 + 重试接缝：用「前 N 次 begin() 抛 1213」的 engine 代理验证
# --------------------------------------------------------------------------- #


class _FailingBegin:
    """``async with`` 进来就抛 1213 的假事务上下文。"""

    def __init__(self, exc: OperationalError) -> None:
        self._exc = exc

    async def __aenter__(self) -> None:
        raise self._exc

    async def __aexit__(self, *exc_info: object) -> bool:
        return False


class _FlakyEngine:
    """engine 代理：在指定的第几次 ``begin()`` 抛可重试错误，其余透传给真 engine。

    用它模拟 MySQL 上的 1213 死锁，验证「维护事务独立 + 插入事务重试」这条接缝，
    而不需要真的跑 MySQL（SQLite 上永远不会自己死锁）。
    事务调用序号是确定的：``create()`` 先跑维护事务，再跑插入事务，失败重试会再占一个序号。
    """

    def __init__(self, real: AsyncEngine, *, fail_on: set[int], code: int = 1213) -> None:
        self._real = real
        self._fail_on = fail_on
        self._code = code
        self.begin_calls = 0

    def begin(self) -> Any:
        self.begin_calls += 1
        if self.begin_calls in self._fail_on:
            return _FailingBegin(_operational_error(self._code))
        return self._real.begin()


def test_维护与插入各自独立事务且1213被有限重试吸收(tmp_path: Path) -> None:
    """维护事务失败 1 次、插入事务失败 1 次 → 建局仍成功，且两次重试都被记录。"""

    async def scenario() -> None:
        engine = create_engine_from_url(_sqlite_url(tmp_path))
        await init_models(engine)
        try:
            # 第 1 次 begin（维护事务）失败 1 次、重试后成功；第 3 次 begin（插入事务）失败 1 次
            flaky = _FlakyEngine(engine, fail_on={1, 3})
            repo = SqlGameRepository(
                flaky,  # type: ignore[arg-type]
                retry_policy=RetryPolicy(attempts=3, base_delay=0.0, max_delay=0.0),
            )
            state = await repo.create(seed=42)
            assert repo.retry_count == 2
            assert repo.retry_counts == {"maintenance": 1, "create": 1}

            # 数据真的落库了（不是重试后丢写）
            healthy = SqlGameRepository(engine)
            fetched = await healthy.get(state.game_id)
            assert fetched.hands == state.hands
            assert fetched.seed == 42
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_维护失败降级但插入持续失败会重试后抛出(tmp_path: Path) -> None:
    """维护动作 best-effort（不抛）；插入在有限重试耗尽后把 1213 抛给调用方。"""

    async def scenario() -> None:
        engine = create_engine_from_url(_sqlite_url(tmp_path, "exhaust.db"))
        await init_models(engine)
        try:
            flaky = _FlakyEngine(engine, fail_on=set(range(1, 11)))
            repo = SqlGameRepository(
                flaky,  # type: ignore[arg-type]
                retry_policy=RetryPolicy(attempts=2, base_delay=0.0, max_delay=0.0),
            )
            with pytest.raises(OperationalError):
                await repo.create(seed=1)
            assert repo.retry_counts == {"maintenance": 1, "create": 1}
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 4. 行为不变：SQLite 上并发建局仍然全部成功
# --------------------------------------------------------------------------- #


def test_SQLite上并发建局全部成功且互不干扰(tmp_path: Path) -> None:
    """20 个协程并发建局：全部成功、game_id 唯一、每局都能读回 136 张牌（SQLite 写串行）。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            results = await asyncio.gather(
                *(repo.create(seed=seed) for seed in range(20)), return_exceptions=True
            )
            failures = [result for result in results if isinstance(result, BaseException)]
            assert not failures, failures
            states = [result for result in results if not isinstance(result, BaseException)]
            assert len({state.game_id for state in states}) == 20
            for state in states:
                fetched = await repo.get(state.game_id)
                total = (
                    len(fetched.wall)
                    + sum(len(hand) for hand in fetched.hands.values())
                    + sum(len(pile) for pile in fetched.discards.values())
                )
                assert total == 136
        finally:
            await engine.dispose()

    asyncio.run(scenario())
