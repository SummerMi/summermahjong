"""对局仓库的抽象接口 + 与存储介质无关的领域操作。

设计（面试可讲）：

1. **依赖倒置**：路由层只依赖 :class:`GameRepository` 这个抽象（``Depends(get_store)``），
   不关心背后是内存字典还是 MySQL 表；换存储介质不需要改任何一行路由代码。
2. **单一真相**：发牌（:func:`new_game_state`）与打牌的状态迁移（:func:`apply_discard`）
   是纯函数，内存实现与 SQL 实现**共用**，避免两套实现随时间漂移出不同行为
   （例如一边改了轮转顺序、另一边忘了改）。存储层只负责“怎么存取”，不负责“规则是什么”。
3. **两种实现**：

   - :class:`app.services.store.GameStore`：内存字典 + ``asyncio.Lock``，零依赖、进程内；
   - :class:`app.services.repository.sql.SqlGameRepository`：SQLAlchemy 2.0 异步，
     本地 SQLite 验证、生产换 ``DATABASE_URL`` 即可切 MySQL。

``GameState`` 是两种实现的「共同语言」：一次读取是一份完整快照，
一次写入是一份完整快照，所以内存与 SQL 实现可以共享同一套校验代码。
"""

from __future__ import annotations

import uuid
from abc import ABC, abstractmethod
from dataclasses import dataclass
from datetime import UTC, datetime

from app.core.errors import AppError, InvalidHandError, TileNotInHandError
from app.services.mahjong.tiles import (
    TILE_KINDS,
    build_deck,
    deal_hands,
    draw_tile,
    shuffle_deck,
    sort_hand,
)

SEATS: tuple[str, ...] = ("east", "south", "west", "north")
"""座位顺序，固定为逆时针东 → 南 → 西 → 北；east 是庄家。"""


@dataclass
class GameState:
    """一局麻将的完整状态快照（牌墙 + 四家手牌 + 四家牌河）。

    这个 dataclass 是存储层的「值对象」：内存实现直接持有它，
    SQL 实现每次读写都把它序列化 / 反序列化到 ``games`` 表的一行。
    """

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
    """最近一次访问的时间戳，用于闲置 TTL 清理。

    时钟由各实现自己决定：内存实现用 ``time.monotonic()``（不受系统时间调整影响），
    SQL 实现用 ``time.time()``（跨进程 / 跨重启可比较）。这个字段只在同一个实现内部
    比较，不会被跨实现混用。
    """

    version: int = 0
    """快照版本号，SQL 实现用它做乐观锁（CAS）；内存实现靠 ``asyncio.Lock``，恒为 0。"""


def new_game_state(seed: int | None = None) -> GameState:
    """洗牌 + 发牌，生成一局新对局的初始快照（不涉及任何存储）。

    内存实现与 SQL 实现都调用这里，所以相同 ``seed`` 在两种存储下得到完全相同的牌局
    —— 这也是「换存储不换行为」的测试依据。

    :param seed: 随机种子；``None`` 表示随机。
    :return: 发牌完成的快照，``east`` 14 张、其余各 13 张，牌墙剩 83 张。
    """
    wall = shuffle_deck(build_deck(), seed=seed)
    # 引擎的 deal_hands 会从 wall 尾部摸牌，调用后 wall 即剩余牌墙（136 - 53 = 83）
    dealt = deal_hands(wall)
    return GameState(
        game_id=uuid.uuid4().hex,
        seed=seed,
        wall=wall,
        hands={seat: sort_hand(hand) for seat, hand in dealt.items()},
        discards={seat: [] for seat in SEATS},
        current_player="east",
        created_at=datetime.now(UTC),
        last_access=0.0,
    )


def apply_discard(state: GameState, seat: str, tile: str) -> str:
    """校验并就地执行一次打牌：出牌 → 进牌河 → 轮转下家 → 下家自动摸一张。

    校验顺序与错误码和内存实现完全一致，纯内存操作、无 I/O，
    调用方负责在锁 / 事务里调用它，并在之后把整份快照写回存储。

    :param state: 对局快照（会被就地修改）。
    :param seat: 出牌座位。
    :param tile: 牌面编码。
    :return: 下一位该出牌的座位。
    :raises InvalidHandError: ``tile`` 不是合法牌面。
    :raises AppError: 还没轮到该座位出牌（``NOT_YOUR_TURN``）。
    :raises TileNotInHandError: ``tile`` 不在该座位手牌里。
    """
    if tile not in TILE_KINDS:
        raise InvalidHandError(f"未知牌：{tile}，合法牌面见 GET /api/v1/mahjong/tiles")
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
    next_player = SEATS[(SEATS.index(seat) + 1) % len(SEATS)]
    state.current_player = next_player

    # 下家自动摸一张；牌墙摸空则只轮转不补牌（演示用，未实现流局/结算）
    drawn = draw_tile(state.wall)
    if drawn is not None:
        state.hands[next_player].append(drawn)
        state.hands[next_player] = sort_hand(state.hands[next_player])
    return next_player


class ConcurrentUpdateError(AppError):
    """乐观锁冲突：读到的快照版本已经过期，说明有并发写入抢先提交。"""

    def __init__(self, game_id: str) -> None:
        super().__init__(
            "GAME_CONFLICT",
            f"对局 {game_id} 正在被并发修改，请重试（乐观锁版本冲突）",
            status_code=409,
        )


class GameRepository(ABC):
    """对局仓库抽象：路由层唯一依赖的接口。

    四个方法覆盖了当前 API 的全部存储需求；新增存储实现（Redis / PostgreSQL）
    只需要实现这四个协程，路由与 schema 都不用动。
    """

    @abstractmethod
    async def create(self, seed: int | None = None) -> GameState:
        """创建并持久化一局新对局，返回初始快照。"""

    @abstractmethod
    async def get(self, game_id: str) -> GameState:
        """读取对局快照，并刷新其最近访问时间。

        :raises GameNotFoundError: 对局不存在或已被清理。
        """

    @abstractmethod
    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        """执行一次打牌，原子地更新对局状态并返回最新快照。

        实现必须保证同一局的并发打牌不会互相覆盖（内存用锁，SQL 用行锁 / 版本号）。
        """

    @abstractmethod
    async def clear(self) -> None:
        """清空所有对局（内存实现清字典；SQL 实现 ``DELETE FROM games``）。"""
