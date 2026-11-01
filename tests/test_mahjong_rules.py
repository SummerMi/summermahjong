"""胡牌判定与听牌（``rules.py``）的单元测试。

覆盖：

- 标准型：4 面子（顺子/刻子）+ 1 将，含三连刻、边张、字牌刻子、字牌作将；
- 七对：含「4 张相同算两对」的豪华七对；
- 未胡：散牌、只有一对、六对差一对、字牌试图组顺子；
- 听牌：两面、边张、单吊、双碰、三面、九面（九莲宝灯）、七对听、不听、死听；
- 边界与非法输入：13/14 张、未知牌、同种超 4 张。
"""

from __future__ import annotations

import pytest

from app.services.mahjong.rules import is_winning_hand, waiting_tiles, winning_pattern
from app.services.mahjong.tiles import TILE_KINDS, InvalidHandError


def 手牌(text: str) -> list[str]:
    """把 ``"123m 456p EE"`` 这类简写解析成牌面列表，方便书写测试用例。

    以 ``m`` / ``p`` / ``s`` 结尾的分组按序数牌展开（``"123m"`` → 1m 2m 3m），
    其余分组按字牌逐字符展开（``"EE"`` → E E）。
    """
    tiles: list[str] = []
    for group in text.split():
        if group[-1] in {"m", "p", "s"}:
            tiles.extend(f"{rank}{group[-1]}" for rank in group[:-1])
        else:
            tiles.extend(group)
    return tiles


# ---------------------------------------------------------------------------
# 标准型胡牌
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("text", "说明"),
    [
        ("123456789m 123p 99s", "四副顺子 + 数牌将"),
        ("111m 222m 333m 444m 55m", "四刻子 + 将牌（三连刻）"),
        ("111222333444m 55m", "四连刻 + 将牌"),
        ("111222333444m 56m", "表面不整齐，实为 123/234/234/456 + 11 将（回溯拆解）"),
        ("123m 123p 123s 789m 99p", "含边张 789 的两面组合 + 将牌"),
        ("123m 456m 789p 234s EE", "字牌作将"),
        ("EEE PPP FFF CCC 11m", "四副字牌刻子 + 数牌将"),
        ("123m 456m 789m 123p 99s", "三副顺子 + 一副顺子 + 将牌"),
    ],
    ids=[
        "四顺子",
        "三连刻",
        "四连刻",
        "回溯拆解",
        "边张与将",
        "字牌将",
        "字牌刻子",
        "全顺子",
    ],
)
def test_标准型胡牌(text: str, 说明: str) -> None:
    """标准型（4 面子 + 1 将）应判定为 "standard"，并同步反映在 is_winning_hand。"""
    tiles = 手牌(text)
    assert len(tiles) == 14, 说明
    assert winning_pattern(tiles) == "standard", 说明
    assert is_winning_hand(tiles) is True, 说明


# ---------------------------------------------------------------------------
# 七对
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("text", "说明"),
    [
        ("11m 22m 33m 44p 55p 66s EE", "七种不同牌各一对"),
        ("1111m 22p 33p 44s 55s EE", "四张相同算两对"),
        ("1111m 2222p 33s 44s EE", "两组四张相同 + 三对（豪华七对）"),
    ],
    ids=["普通七对", "四张算两对", "豪华七对"],
)
def test_七对胡牌(text: str, 说明: str) -> None:
    """七对（每种牌偶数张、共 7 对）应判定为 "seven_pairs"。"""
    tiles = 手牌(text)
    assert len(tiles) == 14, 说明
    assert winning_pattern(tiles) == "seven_pairs", 说明
    assert is_winning_hand(tiles) is True, 说明


def test_七对与标准型同时成立时优先返回标准型() -> None:
    """11223344556677m 既是七对也可拆成 123/123/456/456 + 77，契约要求返回标准型。

    这是实现里明确记录的选择：标准型更基础，七对只是特殊分支；
    若产品需要展示「更稀有的牌型」，只需在这里调整优先级即可。
    """
    tiles = 手牌("11223344556677m")
    assert winning_pattern(tiles) == "standard"
    assert is_winning_hand(tiles) is True


# ---------------------------------------------------------------------------
# 未胡
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("text", "说明"),
    [
        ("1m 4m 7m 1p 4p 7p 1s 4s 7s E S W N P", "14 张全不相同的散牌（无将牌）"),
        ("1m 4m 7m 1p 4p 7p 1s 4s 7s E S W W N", "仅有一对字牌，其余无法组面子"),
        ("11m 22m 33m 44p 55p 66s 1s 2s", "六对 + 两张单牌，还差一对"),
        ("E S W PPP FFF CCC 11m", "东、南、西不是面子：字牌不能组顺子"),
        ("111m 222p 333s 55p 1m 2m 3p", "1m 2m 3p 跨花色，不是顺子"),
    ],
    ids=["散牌", "仅一对", "多对子未成胡", "字牌不能组顺", "顺子不能跨花色"],
)
def test_未胡牌(text: str, 说明: str) -> None:
    """不满足标准型也不满足七对时返回 None，is_winning_hand 为 False。"""
    tiles = 手牌(text)
    assert len(tiles) == 14, 说明
    assert winning_pattern(tiles) is None, 说明
    assert is_winning_hand(tiles) is False, 说明


# ---------------------------------------------------------------------------
# 听牌
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("text", "expected", "说明"),
    [
        ("123m 456m 789m 11p 45s", ["3s", "6s"], "两面听：45s 等 3s/6s"),
        ("123m 456m 789m 11p 12s", ["3s"], "边张听：12s 只等 3s"),
        ("123m 456m 789m 123p 9s", ["9s"], "单吊将：9s 成对"),
        ("123456789m 11p 99s", ["1p", "9s"], "双碰听：1p 或 9s 成刻"),
        ("12345678m 123p 99s", ["3m", "6m", "9m"], "三面听：12345678m 的经典三面"),
        ("11m 22m 33m 44p 55p 66s E", ["E"], "七对听：补一张 E 即七对"),
        ("1111m 22p 33p 44s 55s E", ["E"], "四张算两对的七对听"),
        ("123m 456m 789m 9p 9p 9s 9s", ["9p", "9s"], "双碰听且结果按牌种顺序升序"),
    ],
    ids=[
        "两面听",
        "边张听",
        "单吊听",
        "双碰听",
        "三面听",
        "七对听",
        "豪华七对听",
        "双碰顺序",
    ],
)
def test_听牌(text: str, expected: list[str], 说明: str) -> None:
    """13 张手牌的听牌必须与期望完全一致（含顺序）。"""
    tiles = 手牌(text)
    assert len(tiles) == 13, 说明
    assert waiting_tiles(tiles) == expected, 说明
    # 听牌结果只含合法牌种，且每种最多一个
    assert all(tile in TILE_KINDS for tile in expected), 说明
    assert len(set(expected)) == len(expected), 说明


def test_九莲宝灯式九面听() -> None:
    """1112345678999m 是九面听：1m~9m 任意一张都能胡，这是听牌算法的压力用例。"""
    tiles = 手牌("1112345678999m")
    assert waiting_tiles(tiles) == [f"{rank}m" for rank in range(1, 10)]


def test_不听牌返回空列表() -> None:
    """13 张互不相干的散牌没有听牌，返回空列表而不是抛异常。"""
    assert waiting_tiles(手牌("1m 4m 7m 1p 4p 7p 1s 4s 7s E S W N")) == []


def test_手牌已有四张的牌种不会被推荐为听牌() -> None:
    """若手牌已有 4 张 9s，则第 5 张 9s 不存在于牌山，不能被推荐。

    这个用例是刻意的陷阱：若实现忽略「剩余张数 ≤ 4」，把 9999s 当成
    「刻子 + 将牌」的一部分，就会错误地报出 9s 这个听牌。
    """
    assert waiting_tiles(手牌("123m 456m 789m 9999s")) == []


def test_听牌计算不修改入参() -> None:
    """waiting_tiles 是纯函数，不得改动传入的手牌列表。"""
    tiles = 手牌("123m 456m 789m 11p 45s")
    original = list(tiles)
    waiting_tiles(tiles)
    assert tiles == original


# ---------------------------------------------------------------------------
# 张数边界与非法输入
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "size",
    [0, 1, 13, 15],
    ids=["0张", "1张", "13张", "15张"],
)
def test_胡牌判定张数不是14张时抛异常(size: int) -> None:
    """winning_pattern / is_winning_hand 只接受 14 张，其余张数抛 InvalidHandError。"""
    hand = list(TILE_KINDS[:size])
    with pytest.raises(InvalidHandError, match="14"):
        winning_pattern(hand)
    with pytest.raises(InvalidHandError, match="14"):
        is_winning_hand(hand)


@pytest.mark.parametrize(
    "size",
    [0, 1, 12, 14],
    ids=["0张", "1张", "12张", "14张"],
)
def test_听牌张数不是13张时抛异常(size: int) -> None:
    """waiting_tiles 只接受 13 张，其余张数抛 InvalidHandError。"""
    with pytest.raises(InvalidHandError, match="13"):
        waiting_tiles(list(TILE_KINDS[:size]))


def test_胡牌判定遇未知牌抛InvalidHandError() -> None:
    """未知牌属于非法手牌，不能静默忽略。"""
    hand = 手牌("123m 456m 789m 11p 99s") + ["0m"]
    assert len(hand) == 14
    with pytest.raises(InvalidHandError, match="未知牌"):
        winning_pattern(hand)


def test_听牌遇未知牌抛InvalidHandError() -> None:
    """13 张里有未知牌时同样抛 InvalidHandError。"""
    hand = list(TILE_KINDS[:12]) + ["0m"]
    assert len(hand) == 13
    with pytest.raises(InvalidHandError, match="未知牌"):
        waiting_tiles(hand)


def test_胡牌判定遇同种超四张抛InvalidHandError() -> None:
    """同种牌超过 4 张在物理上不存在，必须视为非法手牌。"""
    hand = ["1m"] * 5 + list(TILE_KINDS[1:10])
    assert len(hand) == 14
    with pytest.raises(InvalidHandError, match="超过每种"):
        winning_pattern(hand)


def test_听牌遇同种超四张抛InvalidHandError() -> None:
    """13 张手牌里同种牌超过 4 张同样非法。"""
    hand = ["1m"] * 5 + list(TILE_KINDS[1:9])
    assert len(hand) == 13
    with pytest.raises(InvalidHandError, match="超过每种"):
        waiting_tiles(hand)
