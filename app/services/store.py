"""内存对局仓库：``asyncio.Lock`` 保护、数量上限、闲置 TTL 清理。

为什么不用数据库：这是一个可演示的实习项目，内存存储零依赖、启动即用；
接口设计成「仓库」形态，生产环境可以整体替换为 Redis（分布式锁 + TTL）
或 MySQL（对局表 + 状态机），路由层不用改。

并发说明：本项目单进程部署，用 ``asyncio.Lock`` 保护「读-改-写」临界区即可；
多进程 / 多实例部署时锁不跨进程，必须换成 Redis 锁或数据库乐观锁。
"""

from __future__ import annotations

import asyncio
import logging
import time
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime

from fastapi import Request

from app.core.errors import (
    AppError,
    GameNotFoundError,
    InvalidHandError,
    TileNotInHandError,
)
from app.services.mahjong.tiles import (
    TILE_KINDS,
    build_deck,
    deal_hands,
    draw_tile,
    shuffle_deck,
    sort_hand,
)

logger = logging.getLogger(__name__)

SEATS: tuple[str, ...] = ("east", "south", "west", "north")
"""座位顺序，固定为逆时针东 → 南 → 西 → 北；east 是庄家。"""


@dataclass
class GameState:
    """一局麻将的完整内存状态（牌墙 + 四家手牌 + 四家牌河）。"""

    game_id: str
    """对局 id，``uuid4().hex``。"""

    seed: int | None
    """创建时使用的随机种子；相同种子可复现同一牌局。"""

    wall: list[str]
    """牌墙（剩余未摸的牌），``draw_tile`` 从尾部弹出。"""

    hands: dict[str, list[str]]
    """四家手牌，key 为座位。"""

    discards: dict[str, list[str]]
    """四家牌河，key 为座位。"""

    current_player: str
    """当前该出牌的座位。"""

    created_at: datetime
    """创建时间（UTC）。"""

    last_access: float
    """最近一次访问的 ``time.monotonic()`` 时间戳，用于 TTL 清理。"""


class GameStore:
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

            wall = shuffle_deck(build_deck(), seed=seed)
            # 引擎的 deal_hands 会从 wall 尾部摸牌，调用后 wall 即剩余牌墙（136 - 53 = 83）
            dealt = deal_hands(wall)
            state = GameState(
                game_id=uuid.uuid4().hex,
                seed=seed,
                wall=wall,
                hands={seat: sort_hand(hand) for seat, hand in dealt.items()},
                discards={seat: [] for seat in SEATS},
                current_player="east",
                created_at=datetime.now(UTC),
                last_access=time.monotonic(),
            )
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

            if tile not in TILE_KINDS:
                raise InvalidHandError(
                    f"未知牌：{tile}，合法牌面见 GET /api/v1/mahjong/tiles"
                )
            if seat != state.current_player:
                raise AppError(
                    "NOT_YOUR_TURN",
                    f"当前轮到 {state.current_player} 出牌，{seat} 不能打牌",
                    status_code=400,
                )
            if tile not in state.hands[seat]:
                raise TileNotInHandError(tile)

            state.hands[seat].remove(tile)
            state.hands[seat] = sort_hand(state.hands[seat])
            state.discards[seat].append(tile)

            # 轮转下家：东 → 南 → 西 → 北 → 东
            next_index = (SEATS.index(seat) + 1) % len(SEATS)
            next_player = SEATS[next_index]
            state.current_player = next_player

            # 下家自动摸一张；牌墙摸空则只轮转不补牌（演示用，未实现流局/结算）
            drawn = draw_tile(state.wall)
            if drawn is not None:
                state.hands[next_player].append(drawn)
                state.hands[next_player] = sort_hand(state.hands[next_player])

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


def get_store(request: Request) -> GameStore:
    """FastAPI 依赖：从 ``app.state`` 取出对局仓库。

    ``GameStore`` 在 ``lifespan`` 启动时创建并挂到 ``app.state.store``，
    这样测试可以给 :func:`app.main.create_app` 传入自定义配置，互不污染。
    """
    store = getattr(request.app.state, "store", None)
    if store is None:  # pragma: no cover - 只有绕过 lifespan 启动才会触发
        raise RuntimeError("GameStore 未初始化：请确认应用通过了 lifespan 启动流程")
    return store
