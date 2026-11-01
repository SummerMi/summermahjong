"""内存对局仓库：``asyncio.Lock`` 保护、数量上限、闲置 TTL 清理。

**默认实现**：``DATABASE_URL`` 为空时应用走这条分支——零依赖、启动即用，
适合本地 demo 与 CI（不需要起数据库）。

为什么还要 SQL 实现：面试与真实业务里「重启后对局还在吗」是必问题。
仓库接口（:class:`~app.services.repository.base.GameRepository`）把「存取」抽象出来，
内存实现与 :class:`~app.services.repository.sql.SqlGameRepository` 可以互换，
路由层一行都不用改。发牌与打牌的规则逻辑放在 ``repository/base.py`` 的纯函数里，
两种实现共用，保证「换存储不换行为」。

并发说明：单进程部署用 ``asyncio.Lock`` 保护「读-改-写」临界区即可；
多进程 / 多实例部署时锁不跨进程，必须换 SQL 实现的行锁 / 乐观锁，或 Redis 分布式锁。
"""

from __future__ import annotations

import asyncio
import logging
import time

from fastapi import Request

from app.core.errors import GameNotFoundError
from app.services.repository.base import (
    SEATS,
    GameRepository,
    GameState,
    apply_discard,
    new_game_state,
)

logger = logging.getLogger(__name__)

__all__ = ["SEATS", "GameRepository", "GameState", "GameStore", "get_store"]


class GameStore(GameRepository):
    """内存对局仓库。

    所有方法都是 ``async`` 的，并用一把 :class:`asyncio.Lock` 保护对局字典，
    避免并发请求在「查 → 改 → 写」之间交错导致状态错乱。
    """

    def __init__(self, max_games: int = 100, ttl_seconds: int = 7200) -> None:
        if max_games < 1:
            raise ValueError("max_games 必须 >= 1")
        if ttl_seconds < 1:
            raise ValueError("ttl_seconds 必须 >= 1")
        self._games: dict[str, GameState] = {}
        self._lock = asyncio.Lock()
        self._max_games = max_games
        self._ttl_seconds = ttl_seconds

    @property
    def max_games(self) -> int:
        """对局数量上限。"""
        return self._max_games

    @property
    def ttl_seconds(self) -> int:
        """对局闲置清理阈值（秒）。"""
        return self._ttl_seconds

    async def create(self, seed: int | None = None) -> GameState:
        """洗牌、发牌并登记一局新对局。

        :param seed: 随机种子；传相同值可复现同一牌局，``None`` 表示随机。
        :return: 新建的对局状态（已完成发牌，``east`` 14 张、其余各 13 张）。
        :raises AppError: 当对局数已达上限且无法淘汰旧对局时（理论上不会发生）。
        """
        async with self._lock:
            self._purge_expired_locked()
            self._make_room_locked()

            state = new_game_state(seed)
            state.last_access = time.monotonic()
            self._games[state.game_id] = state
            logger.info(
                "创建对局 game_id=%s seed=%s wall_remaining=%d", state.game_id, seed, len(state.wall)
            )
            return state

    async def get(self, game_id: str) -> GameState:
        """按 id 取对局，并刷新其最近访问时间。

        :raises GameNotFoundError: 对局不存在或已被 TTL 清理。
        """
        async with self._lock:
            self._purge_expired_locked()
            state = self._games.get(game_id)
            if state is None:
                raise GameNotFoundError(game_id)
            state.last_access = time.monotonic()
            return state

    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        """执行一次打牌：校验 → 移入牌河 → 轮转下家 → 下家摸牌。

        :raises GameNotFoundError: 对局不存在。
        :raises InvalidHandError: ``tile`` 不是合法牌面。
        :raises TileNotInHandError: ``tile`` 不在该座位手牌里。
        :raises AppError: 还没轮到该座位出牌（``NOT_YOUR_TURN``）。
        """
        async with self._lock:
            self._purge_expired_locked()
            state = self._games.get(game_id)
            if state is None:
                raise GameNotFoundError(game_id)
            state.last_access = time.monotonic()

            next_player = apply_discard(state, seat, tile)

            logger.info(
                "打牌 game_id=%s seat=%s tile=%s next=%s wall_remaining=%d",
                game_id,
                seat,
                tile,
                next_player,
                len(state.wall),
            )
            return state

    async def clear(self) -> None:
        """清空所有对局（应用关闭时调用）。"""
        async with self._lock:
            self._games.clear()

    def _purge_expired_locked(self) -> None:
        """清理闲置超时的对局；调用方必须已持有锁。"""
        now = time.monotonic()
        expired = [
            game_id
            for game_id, state in self._games.items()
            if now - state.last_access > self._ttl_seconds
        ]
        for game_id in expired:
            self._games.pop(game_id, None)
        if expired:
            logger.info("TTL 清理对局 %d 局，剩余 %d 局", len(expired), len(self._games))

    def _make_room_locked(self) -> None:
        """对局达到上限时淘汰最久未访问的一局；调用方必须已持有锁。"""
        while len(self._games) >= self._max_games:
            oldest_id = min(self._games, key=lambda game_id: self._games[game_id].last_access)
            self._games.pop(oldest_id, None)
            logger.warning("对局数达到上限 %d，淘汰最久未访问的对局 %s", self._max_games, oldest_id)


def get_store(request: Request) -> GameRepository:
    """FastAPI 依赖：返回当前配置对应的对局仓库实现。

    ``lifespan`` 启动时按 ``settings.database_url`` 选定实现
    （空 → :class:`GameStore` 内存；非空 → ``SqlGameRepository`` 关系库），
    挂到 ``app.state.store``；这里只负责取出。路由因此只依赖 ``GameRepository``
    抽象，对「内存还是数据库」零感知——这就是依赖倒置在真实项目里的样子。

    :raises RuntimeError: 没走 lifespan（例如绕过 ``create_app`` 直接构造请求）。
    """
    store = getattr(request.app.state, "store", None)
    if store is None:  # pragma: no cover - 只有绕过 lifespan 启动才会触发
        raise RuntimeError("GameStore 未初始化：请确认应用通过了 lifespan 启动流程")
    return store
