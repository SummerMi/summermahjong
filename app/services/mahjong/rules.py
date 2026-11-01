"""胡牌判定与听牌计算：34 种牌的定长计数数组 + 递归回溯拆解。

整体思路（面试可直接讲）：

1. 把一手牌映射成 **34 个整数** 的计数数组（下标即 :data:`TILE_KINDS` 顺序），
   比 ``dict`` 更省内存、无哈希开销，且「按牌序从小到大扫描」天然可行；
2. 标准型 = 4 个面子 + 1 个将牌。先枚举将牌（任一张 ≥ 2 张的牌），扣掉后将牌，
   再从最小的非零牌开始递归拆面子：这一步只能选刻子或顺子，且顺子必须同花色
   连续三张，因此字牌永远无法组成顺子；
3. 七对单独判断：所有牌种计数都是偶数且总对数恰为 7（4 张相同算两对）；
4. 听牌 = 对 34 种牌逐一「试加一张」，再用上面的判定看是否胡牌；
   手牌中已有 4 张的牌种直接跳过（一副牌只有 4 张）。

回溯只发生在「某张牌可以先当刻子也可以当顺子」的少数分支上，
14 张手牌的搜索空间极小，实测单次判定在微秒级。
"""

from __future__ import annotations

from collections.abc import Sequence
from typing import Final

from .tiles import (
    TILE_INDEX,
    TILE_KINDS,
    TILES_PER_KIND,
    validate_hand,
)

__all__ = [
    "is_winning_hand",
    "waiting_tiles",
    "winning_pattern",
]

# 标准手牌 14 张、听牌手牌 13 张。
_HAND_SIZE_WIN: Final[int] = 14
_HAND_SIZE_TENPAI: Final[int] = 13
# 标准型需要的面子数与牌种总数。
_MELDS_REQUIRED: Final[int] = 4
_KIND_COUNT: Final[int] = len(TILE_KINDS)
# 前 27 种是序数牌（万筒条各 9 种），后 7 种是字牌（不能组顺子）。
_SUITED_KIND_COUNT: Final[int] = 27
_SEVEN_PAIRS: Final[int] = 7


def _to_counts(hand: Sequence[str]) -> list[int]:
    """把手牌转换成 34 长度的计数数组；调用前须已通过 :func:`validate_hand`。"""
    counts = [0] * _KIND_COUNT
    for tile in hand:
        counts[TILE_INDEX[tile]] += 1
    return counts


def _can_form_melds(counts: list[int], melds_left: int) -> bool:
    """判断计数数组能否恰好拆成 ``melds_left`` 个面子（刻子或顺子）。

    会就地修改 ``counts`` 并在返回前恢复原状，因此可安全复用。

    Args:
        counts: 34 长度计数数组（此时已扣掉将牌）。
        melds_left: 还需要拆出的面子数量。
    """
    if melds_left == 0:
        # 面子数量刚好用完时，必须一张不剩才算拆解成功。
        return not any(counts)

    first = -1
    for index, count in enumerate(counts):
        if count:
            first = index
            break
    if first < 0:
        return False

    # 分支一：把最小非零牌当刻子（三张相同）。
    if counts[first] >= 3:
        counts[first] -= 3
        if _can_form_melds(counts, melds_left - 1):
            counts[first] += 3
            return True
        counts[first] += 3

    # 分支二：把最小非零牌当顺子的首张；字牌与跨花色都不允许。
    rank_in_suit = first % 9
    if (
        first < _SUITED_KIND_COUNT
        and rank_in_suit <= 6  # 首张最大只能是 7，才能凑出 7-8-9
        and counts[first + 1] > 0
        and counts[first + 2] > 0
    ):
        counts[first] -= 1
        counts[first + 1] -= 1
        counts[first + 2] -= 1
        if _can_form_melds(counts, melds_left - 1):
            counts[first] += 1
            counts[first + 1] += 1
            counts[first + 2] += 1
            return True
        counts[first] += 1
        counts[first + 1] += 1
        counts[first + 2] += 1

    return False


def _is_standard(counts: list[int]) -> bool:
    """判断计数数组是否满足标准型（4 面子 + 1 将）。"""
    for pair_index in range(_KIND_COUNT):
        if counts[pair_index] < 2:
            continue
        counts[pair_index] -= 2
        formed = _can_form_melds(counts, _MELDS_REQUIRED)
        counts[pair_index] += 2
        if formed:
            return True
    return False


def _is_seven_pairs(counts: list[int]) -> bool:
    """判断计数数组是否满足七对（每种牌都是偶数张且总对数恰为 7）。

    允许 4 张相同算作两对；14 张牌下「全偶数 + 7 对」等价于 7 个不同对子，
    唯一的例外是四张相同，这正是推倒胡里认可的「豪华七对」基础形。
    """
    pair_count = 0
    for count in counts:
        if count % 2 != 0:
            return False
        pair_count += count // 2
    return pair_count == _SEVEN_PAIRS


def winning_pattern(hand: list[str]) -> str | None:
    """判定 14 张手牌的胡牌牌型。

    Args:
        hand: 14 张手牌（顺序无关）。

    Returns:
        ``"standard"``（4 面子 + 1 将）、``"seven_pairs"``（七对）或
        ``None``（未胡）。两者同时成立时返回 ``"standard"``，
        因为标准型是更基础的定义，七对只是它的一个特殊分支。

    Raises:
        InvalidHandError: 张数不是 14、含未知牌或同种牌超过 4 张。
    """
    validate_hand(hand, allowed_lengths=(_HAND_SIZE_WIN,))
    counts = _to_counts(hand)
    if _is_standard(counts):
        return "standard"
    if _is_seven_pairs(counts):
        return "seven_pairs"
    return None


def is_winning_hand(hand: list[str]) -> bool:
    """判断手牌是否胡牌（标准型或七对），等价于 ``winning_pattern(hand) is not None``。

    Raises:
        InvalidHandError: 张数不是 14、含未知牌或同种牌超过 4 张。
    """
    return winning_pattern(hand) is not None


def waiting_tiles(hand: list[str]) -> list[str]:
    """计算 13 张手牌的听牌，即「再摸哪些牌就能胡」。

    Args:
        hand: 13 张手牌（顺序无关）。

    Returns:
        能胡的牌种列表，按 :data:`TILE_KINDS` 顺序（万 → 筒 → 条 → 字牌）升序；
        空列表表示不听牌。候选牌若手牌中已有 4 张则跳过（牌山不存在第 5 张）。

    Raises:
        InvalidHandError: 张数不是 13、含未知牌或同种牌超过 4 张。
    """
    validate_hand(hand, allowed_lengths=(_HAND_SIZE_TENPAI,))
    counts = _to_counts(hand)
    waits: list[str] = []
    for index, tile in enumerate(TILE_KINDS):
        if counts[index] >= TILES_PER_KIND:
            continue
        counts[index] += 1
        if _is_standard(counts) or _is_seven_pairs(counts):
            waits.append(tile)
        counts[index] -= 1
    return waits
