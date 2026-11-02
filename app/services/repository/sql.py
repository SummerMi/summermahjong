"""SQLAlchemy 2.0 异步实现：把对局快照持久化到关系库。

支持范围（生产 / 演示）：

- 本地验证：``sqlite+aiosqlite:///./games.db``（文件库，重启后数据仍在）；
- 生产切换：``mysql+aiomysql://user:pass@host:3306/mahjong``——代码一行不改，只换
  ``DATABASE_URL``（表结构用同一份 ``Base.metadata``，``JSON`` 列在 MySQL 上即原生 ``JSON``）。

并发控制（面试重点）：

- MySQL / PostgreSQL 这类支持行锁的库：``SELECT ... FOR UPDATE`` 锁住目标行，
  事务提交前其他请求的读会等待，天然串行化同一局的写操作；
- SQLite 没有行锁（写是整库串行，且读锁升级写锁可能直接返回 ``database is locked``），
  所以同一段代码里再叠加**乐观锁**：``UPDATE ... WHERE version = :old_version``，
  版本不匹配说明有人抢先提交，抛 409 ``GAME_CONFLICT`` 让客户端重试。
  两层保护下，SQLite 上并发打牌也不会出现「后写覆盖先写」的状态错乱。

MySQL 实测后修掉的两个缺陷（原始证据见 ``docs/mysql-实测报告.md``，复测见 §10）：

1. **``last_access`` 用 :class:`~sqlalchemy.Double` 而不是 ``Float``**：SQLAlchemy 的
   ``Float`` 在 MySQL 上映射成 4 字节 ``FLOAT``，存 epoch 秒（约 1.79e9）时 ULP 约 128 秒，
   再加上 MySQL 5.7 按 6 位有效数字回读，误差能达到小时级 → TTL 清理与 LRU 排序失真。
   ``Double`` 在 MySQL 上是 8 字节 ``DOUBLE``，SQLite 上仍是 8 字节 REAL，两边语义一致。
2. **TTL 清理 / LRU 淘汰移出建局事务 + 有限重试**：MySQL 5.7 在默认 ``REPEATABLE READ`` 下，
   ``DELETE ... WHERE last_access < ?`` 会对 ``ix_games_last_access`` 加 next-key 锁；
   如果它和 ``INSERT`` 在同一个事务里，并发建局会互相等对方持有的索引锁 → 1213 死锁。
   现在维护动作走**独立短事务**（提交后才开插入事务，锁不叠加，结构上消除锁环），
   外加 :class:`RetryPolicy` 对 1213 / 1205 做有限次「指数退避 + 抖动」重试兜底。
"""

from __future__ import annotations

import asyncio
import logging
import random
import time
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Any, TypeVar

from sqlalchemy import (
    JSON,
    DateTime,
    Double,
    Integer,
    String,
    delete,
    func,
    insert,
    select,
    update,
)
from sqlalchemy.exc import OperationalError
from sqlalchemy.ext.asyncio import AsyncConnection, AsyncEngine, create_async_engine
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column
from sqlalchemy.pool import StaticPool

from app.core.errors import GameNotFoundError
from app.services.repository.base import (
    ConcurrentUpdateError,
    GameRepository,
    GameState,
    apply_discard,
    new_game_state,
)

logger = logging.getLogger(__name__)

_DEADLOCK_CODE = 1213
"""MySQL 死锁：InnoDB 已回滚了本事务，重试是安全的。"""

_LOCK_WAIT_TIMEOUT_CODE = 1205
"""MySQL 锁等待超时：本语句失败但事务可回滚重试，与 1213 同样处理。"""

_RETRYABLE_ERROR_CODES = frozenset({_DEADLOCK_CODE, _LOCK_WAIT_TIMEOUT_CODE})

T = TypeVar("T")

OnRetry = Callable[[str, int, OperationalError], None]
"""重试观测钩子：``(操作名, 第几次失败, 异常)``，用于日志 / 指标 / 测试统计。"""


@dataclass(frozen=True)
class RetryPolicy:
    """可重试错误的**有限**重试策略：指数退避 + 抖动，绝无无限重试。

    只重试 MySQL 的 1213（死锁）与 1205（锁等待超时）——这两类错误在 InnoDB 里已经把
    当前事务回滚了，整体重来一遍是安全的；业务异常（``AppError``）一律不重试。
    退避里加抖动是为了让并发失败的事务错开重来，避免「同一时刻集体重试」再次撞死。

    :param attempts: 总尝试次数（含首次），3 表示最多重试 2 次。
    :param base_delay: 第一次退避的基准秒数（之后按 2 的幂递增）。
    :param max_delay: 单次退避上限，防止参数配大后拖死请求。
    :param jitter: 抖动比例，实际等待 = 退避值 × ``U(1 - jitter, 1 + jitter)``。
    """

    attempts: int = 3
    base_delay: float = 0.02
    max_delay: float = 0.2
    jitter: float = 0.5

    def __post_init__(self) -> None:
        if self.attempts < 1:
            raise ValueError("attempts 必须 >= 1")
        if self.base_delay < 0:
            raise ValueError("base_delay 不能为负")
        if self.max_delay < self.base_delay:
            raise ValueError("max_delay 必须 >= base_delay")
        if not 0.0 <= self.jitter <= 1.0:
            raise ValueError("jitter 必须落在 [0, 1]")

    def backoff(self, attempt: int) -> float:
        """第 ``attempt`` 次失败后的等待秒数（指数退避 + 抖动）。

        :param attempt: 从 1 开始计数的失败次数。
        """
        raw = min(self.base_delay * (2 ** (attempt - 1)), self.max_delay)
        return max(0.0, raw * (1.0 + random.uniform(-self.jitter, self.jitter)))

    @staticmethod
    def is_retryable(exc: BaseException) -> bool:
        """判断异常是否是「重试安全」的 MySQL 死锁 / 锁等待超时。"""
        if not isinstance(exc, OperationalError):
            return False
        orig = exc.orig
        args = getattr(orig, "args", ())
        if args and isinstance(args[0], int) and args[0] in _RETRYABLE_ERROR_CODES:
            return True
        # 驱动 / 代理包装层可能吞掉 orig.args，用错误码文本兜底（1213 / 1205 足够独特）
        text = str(exc)
        return any(str(code) in text for code in _RETRYABLE_ERROR_CODES)

    async def run(
        self,
        operation: str,
        func: Callable[[], Awaitable[T]],
        *,
        on_retry: OnRetry | None = None,
        best_effort: bool = False,
    ) -> T | None:
        """执行 ``func``，遇到可重试错误时按策略退避重试。

        :param operation: 操作名（只用于日志 / 指标，例如 ``create`` / ``discard``）。
        :param func: 一次完整的「开事务 → 干活 → 提交」闭包。
        :param on_retry: 每次决定重试时回调（记录次数、上报指标）。
        :param best_effort: TTL / LRU 这类「做不成也不该拖垮请求」的维护动作传 ``True``：
            重试耗尽或不可重试时只记日志并返回 ``None``，不向上抛。
        :return: ``func`` 的返回值；``best_effort=True`` 且放弃时返回 ``None``。
        """
        for attempt in range(1, self.attempts + 1):
            try:
                return await func()
            except OperationalError as exc:
                retryable = self.is_retryable(exc)
                exhausted = attempt >= self.attempts
                if not retryable or exhausted:
                    if best_effort:
                        logger.warning(
                            "%s 失败（可重试=%s，已尝试 %d/%d 次），本次跳过：%s",
                            operation,
                            retryable,
                            attempt,
                            self.attempts,
                            exc,
                        )
                        return None
                    raise
                if on_retry is not None:
                    on_retry(operation, attempt, exc)
                delay = self.backoff(attempt)
                logger.warning(
                    "%s 触发可重试错误（第 %d/%d 次尝试），%.3fs 后重试：%s",
                    operation,
                    attempt,
                    self.attempts,
                    delay,
                    str(exc).splitlines()[0],
                )
                await asyncio.sleep(delay)
        raise AssertionError("unreachable")  # pragma: no cover - attempts >= 1 已保证有返回


class Base(DeclarativeBase):
    """持久化模型的声明基类；未来新增表（用户、战绩）都继承它。"""


class GameRow(Base):
    """``games`` 表：一行 = 一局麻将的完整快照。

    为什么用 JSON 列存 ``wall`` / ``hands`` / ``discards`` 而不是拆成
    ``tiles`` 明细表：对局状态是「一次写入、整体读取」的聚合根，
    拆表会带来 N+1 查询和事务范围膨胀；MySQL 5.7+/SQLite 都支持 JSON，
    保持聚合根完整更符合领域模型（取舍见 ``docs/adr-0002-持久化选型.md``）。
    """

    __tablename__ = "games"

    game_id: Mapped[str] = mapped_column(String(32), primary_key=True)
    seed: Mapped[int | None] = mapped_column(Integer, nullable=True)
    wall: Mapped[list[str]] = mapped_column(JSON, nullable=False)
    hands: Mapped[dict[str, list[str]]] = mapped_column(JSON, nullable=False)
    discards: Mapped[dict[str, list[str]]] = mapped_column(JSON, nullable=False)
    current_player: Mapped[str] = mapped_column(String(8), nullable=False)
    created_at: Mapped[datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    # 必须是 8 字节 Double：MySQL 的 Float 是 float4，存 epoch 秒会被量化到 128 秒步长，
    # 再叠加 5.7 文本协议 6 位有效数字回读，TTL / LRU 会整体失真（报告 §6.1）。
    last_access: Mapped[float] = mapped_column(Double, nullable=False, index=True)
    version: Mapped[int] = mapped_column(Integer, nullable=False, default=0)


def create_engine_from_url(database_url: str, *, echo: bool = False) -> AsyncEngine:
    """按 DSN 创建异步 engine。

    ``sqlite+aiosqlite:///:memory:`` 必须用 :class:`StaticPool`：
    内存库的生命周期绑在「一条连接」上，默认连接池每次新建连接都会得到一个空库，
    建完表立刻「丢表」。文件库 / MySQL 走默认连接池即可。
    """
    kwargs: dict[str, Any] = {"echo": echo}
    if ":memory:" in database_url:
        kwargs["poolclass"] = StaticPool
        kwargs["connect_args"] = {"check_same_thread": False}
    return create_async_engine(database_url, **kwargs)


async def init_models(engine: AsyncEngine) -> None:
    """应用启动时建表（幂等：``CREATE TABLE IF NOT EXISTS``）。

    演示 / 小项目用 ``create_all`` 足够；正式项目应换成 Alembic 迁移，
    否则改字段只能手工 ``ALTER TABLE``，没法回滚也没法在不同环境复现。
    注意：``create_all`` **不会**改已存在表的列类型——老库（``last_access`` 是 FLOAT）
    需要一次手工 ``ALTER TABLE games MODIFY last_access DOUBLE NOT NULL``（见报告 §10）。
    """
    async with engine.begin() as conn:
        await conn.run_sync(Base.metadata.create_all)


def mask_dsn(database_url: str) -> str:
    """把 DSN 里的密码替换成 ``***``，避免启动日志泄漏数据库凭据。"""
    if "://" not in database_url:
        return database_url
    scheme, rest = database_url.split("://", 1)
    if "@" not in rest:
        return database_url
    credentials, host = rest.rsplit("@", 1)
    if ":" in credentials:
        credentials = credentials.split(":", 1)[0] + ":***"
    return f"{scheme}://{credentials}@{host}"


def _row_to_state(row: Mapping[str, Any]) -> GameState:
    """数据库行（``RowMapping``）→ 领域快照。

    这里用 Core 连接直接执行 ORM ``select``，拿到的是列映射而不是 ORM 实例
    （ORM 实例化需要 Session），因此按列名取值。JSON 列做浅拷贝，
    避免后续 ``apply_discard`` 就地修改时改到映射里的容器。
    """
    created_at = row["created_at"]
    if created_at.tzinfo is None:  # SQLite / MySQL 读回来是 naive，统一补成 UTC
        created_at = created_at.replace(tzinfo=UTC)
    return GameState(
        game_id=row["game_id"],
        seed=row["seed"],
        wall=list(row["wall"]),
        hands={seat: list(tiles) for seat, tiles in row["hands"].items()},
        discards={seat: list(tiles) for seat, tiles in row["discards"].items()},
        current_player=row["current_player"],
        created_at=created_at,
        last_access=row["last_access"],
        version=row["version"],
    )


class SqlGameRepository(GameRepository):
    """基于 SQLAlchemy 异步 engine 的对局仓库。

    写操作都在短事务里完成「读 → 校验 → 写」，保证一次打牌要么整份快照写成功、
    要么什么都没发生；TTL 清理 / LRU 淘汰这类**维护动作**放在独立事务里先提交，
    避免「事务内先删后插」在 MySQL 上形成间隙锁死锁（见模块 docstring）。

    :param engine: 异步 engine（生命周期由调用方 / lifespan 管理）。
    :param max_games: 对局数量上限（LRU 淘汰按 ``last_access`` 最旧优先）。
    :param ttl_seconds: 闲置多久算过期（秒）。
    :param retry_policy: 可重试错误的退避策略；省略用默认（最多 3 次尝试）。
    :param on_retry: 重试观测钩子，便于接入日志 / Prometheus / 测试统计。
    """

    def __init__(
        self,
        engine: AsyncEngine,
        *,
        max_games: int = 100,
        ttl_seconds: int = 7200,
        retry_policy: RetryPolicy | None = None,
        on_retry: OnRetry | None = None,
    ) -> None:
        if max_games < 1:
            raise ValueError("max_games 必须 >= 1")
        if ttl_seconds < 1:
            raise ValueError("ttl_seconds 必须 >= 1")
        self._engine = engine
        self._max_games = max_games
        self._ttl_seconds = ttl_seconds
        self._retry_policy = retry_policy or RetryPolicy()
        self._on_retry = on_retry
        self._retry_count = 0
        self._retry_counts: dict[str, int] = {}

    @property
    def engine(self) -> AsyncEngine:
        """底层 engine（lifespan 关闭时 dispose 用）。"""
        return self._engine

    @property
    def max_games(self) -> int:
        """对局数量上限。"""
        return self._max_games

    @property
    def ttl_seconds(self) -> int:
        """对局闲置清理阈值（秒）。"""
        return self._ttl_seconds

    @property
    def retry_policy(self) -> RetryPolicy:
        """当前生效的重试策略（只读，便于测试断言与运维观测）。"""
        return self._retry_policy

    @property
    def retry_count(self) -> int:
        """累计重试次数（进程内计数，用于日志 / 指标）。"""
        return self._retry_count

    @property
    def retry_counts(self) -> dict[str, int]:
        """按操作名分组的累计重试次数（``create`` / ``discard`` / ``get`` / ``maintenance``）。"""
        return dict(self._retry_counts)

    def _record_retry(self, operation: str, attempt: int, exc: OperationalError) -> None:
        """统计 + 转发给外部观测钩子。"""
        self._retry_count += 1
        self._retry_counts[operation] = self._retry_counts.get(operation, 0) + 1
        if self._on_retry is not None:
            self._on_retry(operation, attempt, exc)

    async def create(self, seed: int | None = None) -> GameState:
        """发牌并插入一行新对局。

        顺序刻意分成两步：

        1. **维护事务**（独立提交）：TTL 清理 + LRU 淘汰；
        2. **插入事务**：只做一条 ``INSERT``。

        MySQL 5.7 的 RR 隔离级别下，第 1 步的范围 ``DELETE`` 会对
        ``ix_games_last_access`` 加 next-key 锁；一旦和 ``INSERT`` 同事务，
        并发建局就会「你等我的间隙锁、我等你的插入意向锁」→ 1213 死锁（报告 §6.2）。
        两步各自提交后，锁在插入开始前就已释放，结构上不再有环。
        """
        await self._maintenance()
        return await self._retry_policy.run(
            "create",
            lambda: self._insert_new_game(seed),
            on_retry=self._record_retry,
        )  # type: ignore[return-value]

    async def get(self, game_id: str) -> GameState:
        """读取对局并刷新 ``last_access``；不存在抛 404。"""
        await self._purge_expired_only()
        return await self._retry_policy.run(
            "get",
            lambda: self._read_and_touch(game_id),
            on_retry=self._record_retry,
        )  # type: ignore[return-value]

    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        """在事务里完成一次打牌。

        读阶段：支持行锁的库用 ``SELECT ... FOR UPDATE`` 锁行（SQLite 自动跳过）；
        写阶段：``WHERE version = old`` 做乐观锁 CAS，版本冲突抛 409 而不是静默覆盖。
        1213 / 1205 会触发有限重试（事务已回滚，重来一次安全）。
        """
        await self._purge_expired_only()
        return await self._retry_policy.run(
            "discard",
            lambda: self._discard_in_transaction(game_id, seat, tile),
            on_retry=self._record_retry,
        )  # type: ignore[return-value]

    async def clear(self) -> None:
        """删除所有对局（测试 / 运维用；正常关闭应用**不会**清库）。"""

        async def clear_all() -> None:
            async with self._engine.begin() as conn:
                await conn.execute(delete(GameRow))

        await self._retry_policy.run("clear", clear_all, on_retry=self._record_retry)

    # ------------------------------------------------------------------ #
    # 维护动作：独立事务，best-effort（做不成不拖垮业务请求）
    # ------------------------------------------------------------------ #

    async def _maintenance(self) -> None:
        """TTL 清理 + LRU 淘汰，一次独立事务。

        可重入：每次都从库里重新算「过期集合 / 最旧若干局」，不依赖内存状态，
        重复调用、并发调用都不会越删（删不到就 ``rowcount = 0``）。
        """

        async def run() -> None:
            async with self._engine.begin() as conn:
                await self._purge_expired(conn)
                await self._evict_overflow(conn)

        await self._retry_policy.run(
            "maintenance", run, on_retry=self._record_retry, best_effort=True
        )

    async def _purge_expired_only(self) -> None:
        """只有 TTL 清理（读 / 打牌路径用），独立事务 + best-effort。"""

        async def run() -> None:
            async with self._engine.begin() as conn:
                await self._purge_expired(conn)

        await self._retry_policy.run(
            "maintenance", run, on_retry=self._record_retry, best_effort=True
        )

    async def _purge_expired(self, conn: AsyncConnection) -> None:
        """删除闲置超过 TTL 的对局，语义与内存实现一致。"""
        threshold = time.time() - self._ttl_seconds
        result = await conn.execute(delete(GameRow).where(GameRow.last_access < threshold))
        if result.rowcount:
            logger.info("TTL 清理对局（SQL） %d 局", result.rowcount)

    async def _evict_overflow(self, conn: AsyncConnection) -> None:
        """对局数达到上限时淘汰最久未访问的若干局（语义与内存实现的 LRU 一致）。"""
        total = (await conn.execute(select(func.count()).select_from(GameRow))).scalar_one()
        if total < self._max_games:
            return
        excess = total - self._max_games + 1
        oldest_ids = list(
            (
                await conn.execute(
                    select(GameRow.game_id).order_by(GameRow.last_access).limit(excess)
                )
            )
            .scalars()
            .all()
        )
        if oldest_ids:
            await conn.execute(delete(GameRow).where(GameRow.game_id.in_(oldest_ids)))
            logger.warning(
                "对局数达到上限 %d，淘汰最久未访问的 %d 局", self._max_games, len(oldest_ids)
            )

    # ------------------------------------------------------------------ #
    # 业务事务：一个事务只做一件事，锁范围尽量小
    # ------------------------------------------------------------------ #

    async def _insert_new_game(self, seed: int | None) -> GameState:
        """只发牌 + 插一行，事务里没有其它删除 / 更新语句。"""
        state = new_game_state(seed)
        state.last_access = time.time()
        async with self._engine.begin() as conn:
            await conn.execute(
                insert(GameRow).values(
                    game_id=state.game_id,
                    seed=state.seed,
                    wall=state.wall,
                    hands=state.hands,
                    discards=state.discards,
                    current_player=state.current_player,
                    created_at=state.created_at,
                    last_access=state.last_access,
                    version=0,
                )
            )
        logger.info(
            "创建对局（SQL） game_id=%s seed=%s wall_remaining=%d",
            state.game_id,
            seed,
            len(state.wall),
        )
        return state

    async def _read_and_touch(self, game_id: str) -> GameState:
        """按主键读 + 刷新 ``last_access``（同一事务，读到的就是写前的快照）。"""
        async with self._engine.begin() as conn:
            row = (
                await conn.execute(select(GameRow).where(GameRow.game_id == game_id))
            ).mappings().one_or_none()
            if row is None:
                raise GameNotFoundError(game_id)
            now = time.time()
            await conn.execute(
                update(GameRow).where(GameRow.game_id == game_id).values(last_access=now)
            )
            state = _row_to_state(row)
            state.last_access = now
            return state

    async def _discard_in_transaction(self, game_id: str, seat: str, tile: str) -> GameState:
        """行锁（MySQL）+ 版本号 CAS（所有方言）下的原子打牌。"""
        async with self._engine.begin() as conn:
            stmt = select(GameRow).where(GameRow.game_id == game_id)
            if conn.dialect.name != "sqlite":
                # MySQL / PostgreSQL：行锁把同一局的并发写串行化（SQLite 不支持行锁）
                stmt = stmt.with_for_update()
            row = (await conn.execute(stmt)).mappings().one_or_none()
            if row is None:
                raise GameNotFoundError(game_id)

            state = _row_to_state(row)
            next_player = apply_discard(state, seat, tile)

            now = time.time()
            result = await conn.execute(
                update(GameRow)
                .where(GameRow.game_id == game_id, GameRow.version == state.version)
                .values(
                    wall=state.wall,
                    hands=state.hands,
                    discards=state.discards,
                    current_player=state.current_player,
                    last_access=now,
                    version=state.version + 1,
                )
            )
            if result.rowcount != 1:
                # 读到的版本已被别人提交改写 → 本次打牌作废，让客户端重试。
                raise ConcurrentUpdateError(game_id)

            state.version += 1
            state.last_access = now
            logger.info(
                "打牌（SQL） game_id=%s seat=%s tile=%s next=%s wall_remaining=%d version=%d",
                game_id,
                seat,
                tile,
                next_player,
                len(state.wall),
                state.version,
            )
            return state
