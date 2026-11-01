"""麻将牌的定义、牌墙构建、洗牌、发牌与手牌工具。

本模块属于领域纯逻辑层：不做任何 I/O、不依赖 Web 框架、不使用全局可变状态，
因此可以被单独单元测试，也适合在面试时白板讲解。

牌编码见 ``docs/engineering-contract.md`` 第 3 节：

- 序数牌 ``"1m"``~``"9m"``（万）、``"1p"``~``"9p"``（筒）、``"1s"``~``"9s"``（条）；
- 字牌固定 7 种、固定顺序 ``E S W N P F C``（东 南 西 北 白 发 中）；
- 每种 4 张，共 34 种 × 4 = 136 张，不含花牌。
"""

from __future__ import annotations

import random
from collections.abc import Mapping, Sequence
from types import MappingProxyType
from typing import Final

__all__ = [
    "DECK_SIZE",
    "TILES_PER_KIND",
    "TILE_INDEX",
    "TILE_KINDS",
    "InvalidHandError",
    "MahjongError",
    "NotEnoughTilesError",
    "build_deck",
    "count_tiles",
    "deal_hands",
    "draw_tile",
    "shuffle_deck",
    "sort_hand",
    "validate_hand",
]

# 34 种牌，元组顺序即「展示 / 排序」顺序：万 → 筒 → 条 → 东南西北白发中。
TILE_KINDS: Final[tuple[str, ...]] = (
    *(f"{rank}m" for rank in range(1, 10)),
    *(f"{rank}p" for rank in range(1, 10)),
    *(f"{rank}s" for rank in range(1, 10)),
    "E",
    "S",
    "W",
    "N",
    "P",
    "F",
    "C",
)

# 每种牌的张数与整副牌张数（不含花牌，故为 34 × 4 = 136）。
TILES_PER_KIND: Final[int] = 4
DECK_SIZE: Final[int] = len(TILE_KINDS) * TILES_PER_KIND

# 牌面 → 34 种牌中的固定下标；用只读映射避免被调用方意外改写。
TILE_INDEX: Final[Mapping[str, int]] = MappingProxyType(
    {tile: index for index, tile in enumerate(TILE_KINDS)}
)

# 默认四个座位，顺序固定为东、南、西、北；第 0 个座位（east）是庄家。
DEFAULT_SEATS: Final[tuple[str, ...]] = ("east", "south", "west", "north")


class MahjongError(ValueError):
    """麻将领域异常基类。

    继承 ``ValueError``，便于 API 层统一映射成 HTTP 400（无需感知领域细节）。
    """


class InvalidHandError(MahjongError):
    """手牌非法：含未知牌、张数不符合预期、同种牌超过 4 张。"""


class NotEnoughTilesError(MahjongError):
    """牌墙张数不足，无法完成摸牌 / 发牌等操作。"""


def validate_hand(
    hand: Sequence[str],
    allowed_lengths: tuple[int, ...] | None = None,
) -> None:
    """校验手牌合法性，非法时抛 :class:`InvalidHandError`。

    Args:
        hand: 待校验的牌面序列，通常为 13 或 14 张。
        allowed_lengths: 允许的张数集合；``None`` 表示不限制张数
            （仅校验牌面与每种牌不超过 4 张）。

    Raises:
        InvalidHandError: 出现未知牌、张数不在 ``allowed_lengths`` 中、
            或同种牌超过 4 张。
    """
    if allowed_lengths is not None and len(hand) not in allowed_lengths:
        expected = " 或 ".join(str(length) for length in allowed_lengths)
        raise InvalidHandError(
            f"手牌张数必须是 {expected}，实际为 {len(hand)} 张"
        )
    counts: dict[str, int] = {}
    for tile in hand:
        if tile not in TILE_INDEX:
            raise InvalidHandError(f"未知牌：{tile!r}")
        counts[tile] = counts.get(tile, 0) + 1
        if counts[tile] > TILES_PER_KIND:
            raise InvalidHandError(
                f"牌 {tile!r} 共有 {counts[tile]} 张，超过每种 {TILES_PER_KIND} 张的上限"
            )


def build_deck() -> list[str]:
    """构建一副完整的 136 张牌（34 种，每种 4 张）。

    返回新列表，调用方可随意修改；顺序按 :data:`TILE_KINDS` 连续排列。
    """
    return [tile for tile in TILE_KINDS for _ in range(TILES_PER_KIND)]


def shuffle_deck(deck: list[str], seed: int | None = None) -> list[str]:
    """返回洗牌后的**新**牌墙，不修改入参。

    随机源用 ``random.Random(seed)`` 而不是模块级 ``random``：
    ``seed`` 相同则结果完全可复现（测试 / 录像回放可依赖），
    同时避免污染全局随机状态。

    Args:
        deck: 待洗牌的牌墙。
        seed: 随机种子；``None`` 表示每次随机。
    """
    shuffled = list(deck)
    random.Random(seed).shuffle(shuffled)
    return shuffled


def draw_tile(wall: list[str]) -> str | None:
    """从牌墙**尾部**摸一张牌；牌墙为空时返回 ``None``。

    会就地缩短 ``wall``（摸牌本身是有状态的，牌墙由上层持有）。
    """
    if not wall:
        return None
    return wall.pop()


def deal_hands(
    wall: list[str],
    seats: tuple[str, ...] = DEFAULT_SEATS,
) -> dict[str, list[str]]:
    """发牌：每家 13 张，庄家（``seats[0]``，默认 ``east``）多摸 1 张共 14 张。

    按「每人一张、轮一圈」的顺序发牌；发完后庄家再摸第 14 张。
    牌墙会被就地消耗，4 家标准局发完后正好剩 136 - 53 = 83 张。

    Args:
        wall: 牌墙，会被就地消耗。
        seats: 座位名序列，默认东南西北四家；``seats[0]`` 视为庄家。

    Returns:
        座位 → 手牌的字典，手牌已按 :data:`TILE_KINDS` 顺序排序。

    Raises:
        MahjongError: 座位为空或座位名重复。
        NotEnoughTilesError: 牌墙不足 ``13 × 家数 + 1`` 张
            （4 家即最少 53 张）。
    """
    seat_list = list(seats)
    if not seat_list:
        raise MahjongError("座位列表不能为空")
    if len(set(seat_list)) != len(seat_list):
        raise MahjongError(f"座位名不能重复：{seat_list}")

    required = 13 * len(seat_list) + 1
    if len(wall) < required:
        raise NotEnoughTilesError(
            f"牌墙剩余 {len(wall)} 张，发牌至少需要 {required} 张"
        )

    hands: dict[str, list[str]] = {seat: [] for seat in seat_list}
    for _ in range(13):
        for seat in seat_list:
            tile = draw_tile(wall)
            # 上面已按张数校验，此处只做类型收窄，正常不会触发。
            if tile is None:  # pragma: no cover - 防御性分支
                raise NotEnoughTilesError("发牌过程中牌墙被意外取空")
            hands[seat].append(tile)

    dealer = seat_list[0]
    dealer_tile = draw_tile(wall)
    if dealer_tile is None:  # pragma: no cover - 防御性分支
        raise NotEnoughTilesError("庄家补摸时牌墙已空")
    hands[dealer].append(dealer_tile)

    return {seat: sort_hand(tiles) for seat, tiles in hands.items()}


def sort_hand(hand: list[str]) -> list[str]:
    """返回按 :data:`TILE_KINDS` 顺序排序的**新**手牌列表，不修改入参。

    排序键即 34 种牌的固定下标，所以「万 → 筒 → 条 → 字牌」，
    字牌内部为「东南西北白发中」，同种牌相邻。

    Raises:
        InvalidHandError: 出现未知牌或同种牌超过 4 张。
    """
    validate_hand(hand)
    return sorted(hand, key=TILE_INDEX.__getitem__)


def count_tiles(hand: list[str]) -> dict[str, int]:
    """统计每种牌的张数，只保留出现过的牌种，键按 :data:`TILE_KINDS` 顺序。

    例：``count_tiles(["E", "1m", "1m"]) == {"1m": 2, "E": 1}``。

    Raises:
        InvalidHandError: 出现未知牌或同种牌超过 4 张。
    """
    validate_hand(hand)
    counts: dict[str, int] = {}
    for tile in hand:
        counts[tile] = counts.get(tile, 0) + 1
    return {tile: counts[tile] for tile in TILE_KINDS if tile in counts}
