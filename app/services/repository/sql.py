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
"""

from __future__ import annotations

import logging
import time
from collections.abc import Mapping
from datetime import UTC, datetime
from typing import Any

from sqlalchemy import (
    JSON,
    DateTime,
    Float,
    Integer,
    String,
    delete,
    func,
    insert,
    select,
    update,
)
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
    last_access: Mapped[float] = mapped_column(Float, nullable=False, index=True)
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

    每次操作开一个短事务，事务内完成「读 → 校验 → 写」，
    保证一次打牌要么整份快照写成功、要么什么都没发生。
    """

    def __init__(self, engine: AsyncEngine, *, max_games: int = 100, ttl_seconds: int = 7200) -> None:
        if max_games < 1:
            raise ValueError("max_games 必须 >= 1")
        if ttl_seconds < 1:
            raise ValueError("ttl_seconds 必须 >= 1")
        self._engine = engine
        self._max_games = max_games
        self._ttl_seconds = ttl_seconds

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

    async def create(self, seed: int | None = None) -> GameState:
        """发牌并插入一行新对局；先按上限淘汰最久未访问的旧对局。"""
        async with self._engine.begin() as conn:
            await self._purge_expired(conn)
            await self._evict_overflow(conn)

            state = new_game_state(seed)
            state.last_access = time.time()
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

    async def get(self, game_id: str) -> GameState:
        """读取对局并刷新 ``last_access``；不存在抛 404。"""
        async with self._engine.begin() as conn:
            await self._purge_expired(conn)
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

    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        """在事务里完成一次打牌。

        读阶段：支持行锁的库用 ``SELECT ... FOR UPDATE`` 锁行（SQLite 自动跳过）；
        写阶段：``WHERE version = old`` 做乐观锁 CAS，版本冲突抛 409 而不是静默覆盖。
        """
        async with self._engine.begin() as conn:
            await self._purge_expired(conn)

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

    async def clear(self) -> None:
        """删除所有对局（测试 / 运维用；正常关闭应用**不会**清库）。"""
        async with self._engine.begin() as conn:
            await conn.execute(delete(GameRow))

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
            logger.warning("对局数达到上限 %d，淘汰最久未访问的 %d 局", self._max_games, len(oldest_ids))
