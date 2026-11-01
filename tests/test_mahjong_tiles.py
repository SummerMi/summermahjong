"""麻将牌与牌墙（``tiles.py``）的单元测试。

覆盖契约 ``docs/engineering-contract.md`` 第 3、4 节：

- 34 种牌、每种 4 张、共 136 张；
- 洗牌可复现、不修改入参；
- 摸牌、发牌（每家 13 张、庄家 14 张、牌墙不足 53 张抛 ``NotEnoughTilesError``）；
- 排序与计数；
- 非法手牌（未知牌 / 张数不对 / 同种超 4 张）抛 ``InvalidHandError``。
"""

from __future__ import annotations

import pytest

from app.services.mahjong.tiles import (
    DECK_SIZE,
    DEFAULT_SEATS,
    TILE_INDEX,
    TILE_KINDS,
    TILES_PER_KIND,
    InvalidHandError,
    MahjongError,
    NotEnoughTilesError,
    build_deck,
    count_tiles,
    deal_hands,
    draw_tile,
    shuffle_deck,
    sort_hand,
    validate_hand,
)


def test_牌种共34种且顺序符合契约() -> None:
    """TILE_KINDS 必须是 34 种牌，顺序为万 → 筒 → 条 → 东南西北白发中。"""
    assert len(TILE_KINDS) == 34
    assert TILE_KINDS[:9] == tuple(f"{rank}m" for rank in range(1, 10))
    assert TILE_KINDS[9:18] == tuple(f"{rank}p" for rank in range(1, 10))
    assert TILE_KINDS[18:27] == tuple(f"{rank}s" for rank in range(1, 10))
    assert TILE_KINDS[27:] == ("E", "S", "W", "N", "P", "F", "C")
    assert len(set(TILE_KINDS)) == 34


def test_牌面下标映射与牌种顺序一一对应且只读() -> None:
    """TILE_INDEX 是只读映射：下标即展示顺序，调用方无法意外篡改全局顺序。"""
    assert TILE_INDEX["1m"] == 0
    assert TILE_INDEX["9m"] == 8
    assert TILE_INDEX["1p"] == 9
    assert TILE_INDEX["9s"] == 26
    assert TILE_INDEX["E"] == 27
    assert TILE_INDEX["C"] == 33
    with pytest.raises(TypeError):
        TILE_INDEX["1m"] = 0


def test_牌墙共136张且34种各4张() -> None:
    """build_deck 返回 136 张牌，每种牌恰好 4 张，不含花牌。"""
    deck = build_deck()
    assert len(deck) == 136
    assert len(deck) == DECK_SIZE
    counts = count_tiles(deck)
    assert set(counts) == set(TILE_KINDS)
    assert all(count == TILES_PER_KIND for count in counts.values())


def test_每次构建牌墙都是独立的新列表() -> None:
    """两次 build_deck 互不影响，牌墙不能被全局状态共享。"""
    first = build_deck()
    second = build_deck()
    assert first is not second
    first.pop()
    assert len(second) == 136


@pytest.mark.parametrize("seed", [0, 1, 42, 2026], ids=["种子0", "种子1", "种子42", "种子2026"])
def test_相同种子洗牌结果可复现(seed: int) -> None:
    """同一 seed 洗同一副牌必须得到完全相同的牌墙（测试/录像回放依赖此性质）。"""
    deck = build_deck()
    first = shuffle_deck(deck, seed=seed)
    second = shuffle_deck(build_deck(), seed=seed)
    assert first == second
    # 洗牌只是换顺序，牌面集合与张数不变
    assert sorted(first) == sorted(deck)
    # 不修改传入的牌墙
    assert deck == build_deck()


def test_不同种子洗牌结果不同() -> None:
    """不同 seed 得到不同排列（136 张牌恰好相同的概率可忽略）。"""
    assert shuffle_deck(build_deck(), seed=42) != shuffle_deck(build_deck(), seed=43)


def test_不指定种子也能洗牌且不丢牌() -> None:
    """seed=None 时随机洗牌，结果仍是完整的一副牌，且返回新列表。"""
    deck = build_deck()
    shuffled = shuffle_deck(deck)
    assert shuffled is not deck
    assert len(shuffled) == 136
    assert sorted(shuffled) == sorted(deck)


def test_摸牌从牌墙尾部取且空墙返回None() -> None:
    """draw_tile 从尾部摸牌并就地缩短牌墙；牌墙为空时返回 None 而不是抛异常。"""
    wall = ["1m", "2m", "3m"]
    assert draw_tile(wall) == "3m"
    assert wall == ["1m", "2m"]
    assert draw_tile(wall) == "2m"
    assert draw_tile(wall) == "1m"
    assert wall == []
    assert draw_tile(wall) is None


def test_发牌每家13张且庄家14张() -> None:
    """默认四家发牌：east（庄家）14 张，其余三家各 13 张，牌墙剩 83 张。"""
    wall = shuffle_deck(build_deck(), seed=7)
    hands = deal_hands(wall)

    assert list(hands) == list(DEFAULT_SEATS)
    assert len(hands["east"]) == 14
    for seat in DEFAULT_SEATS[1:]:
        assert len(hands[seat]) == 13
    assert len(wall) == 83
    # 返回的手牌已排序，便于展示与比较
    for seat in DEFAULT_SEATS:
        assert hands[seat] == sort_hand(hands[seat])


def test_发牌恰好消耗五十三张牌且每种不超四张() -> None:
    """发牌后牌墙少 53 张；全场发出的牌没有哪一种超过 4 张。"""
    wall = build_deck()
    dealt_total_before = len(wall)
    hands = deal_hands(wall)
    dealt = [tile for tiles in hands.values() for tile in tiles]

    assert len(dealt) == 53
    assert len(wall) == dealt_total_before - 53
    counts = count_tiles(dealt)
    assert all(count <= TILES_PER_KIND for count in counts.values())


def test_牌墙恰好53张时可以完成发牌() -> None:
    """边界值：牌墙正好 53 张时发牌成功，且牌墙被取空。"""
    wall = build_deck()[:53]
    hands = deal_hands(wall)
    assert len(wall) == 0
    assert len(hands["east"]) == 14
    assert sum(len(tiles) for tiles in hands.values()) == 53


@pytest.mark.parametrize("size", [0, 1, 52], ids=["空牌墙", "仅1张", "差1张"])
def test_牌墙不足53张时发牌抛NotEnoughTilesError(size: int) -> None:
    """牌墙不足 13×4+1=53 张时必须抛 NotEnoughTilesError（继承自 MahjongError）。"""
    with pytest.raises(NotEnoughTilesError, match="至少需要 53 张|发牌至少需要 53 张"):
        deal_hands(build_deck()[:size])


def test_发牌支持自定义座位且首座位为庄家() -> None:
    """座位名可自定义；seats[0] 视为庄家，多摸第 14 张。"""
    seats = ("甲", "乙", "丙", "丁")
    hands = deal_hands(build_deck(), seats=seats)
    assert list(hands) == list(seats)
    assert len(hands["甲"]) == 14
    assert all(len(hands[seat]) == 13 for seat in seats[1:])


@pytest.mark.parametrize(
    "seats",
    [("east", "east", "west", "north"), ()],
    ids=["座位名重复", "座位为空"],
)
def test_座位配置非法时发牌抛MahjongError(seats: tuple[str, ...]) -> None:
    """座位名重复或为空属于调用方错误，抛 MahjongError 而不是默默发牌。"""
    with pytest.raises(MahjongError):
        deal_hands(build_deck(), seats=seats)


def test_相同种子发牌结果完全可复现() -> None:
    """同一种子 → 同一牌墙 → 每一家手牌完全一致（演示与回归测试的基础）。"""
    first = deal_hands(shuffle_deck(build_deck(), seed=2026))
    second = deal_hands(shuffle_deck(build_deck(), seed=2026))
    assert first == second


def test_手牌排序按万筒条字牌固定顺序() -> None:
    """sort_hand 按 TILE_KINDS 顺序排序：万 → 筒 → 条 → 东南西北白发中，且不修改入参。"""
    hand = ["P", "9s", "1m", "E", "1m", "C", "5p"]
    original = list(hand)
    assert sort_hand(hand) == ["1m", "1m", "5p", "9s", "E", "P", "C"]
    assert hand == original


def test_排序空手牌返回空列表() -> None:
    """空手牌是合法的（张数不受限），排序结果为空列表。"""
    assert sort_hand([]) == []


def test_计数只保留出现过的牌种且按固定顺序() -> None:
    """count_tiles 只统计出现过的牌，键按 TILE_KINDS 顺序排列。"""
    hand = ["E", "9s", "1m", "1m"]
    counts = count_tiles(hand)
    assert counts == {"1m": 2, "9s": 1, "E": 1}
    assert list(counts) == ["1m", "9s", "E"]


@pytest.mark.parametrize(
    ("hand", "fragment"),
    [
        (["1m"] * 5, "超过每种"),
        (["0m", "1m"], "未知牌"),
        (["10m", "1m"], "未知牌"),
        (["east", "1m"], "未知牌"),
        (["1M", "1m"], "未知牌"),
        (["1m ", "1m"], "未知牌"),
        (["", "1m"], "未知牌"),
    ],
    ids=["同种5张", "0m非法", "10m非法", "座位名当牌", "大写M非法", "带空格非法", "空字符串非法"],
)
def test_非法牌面校验抛InvalidHandError(hand: list[str], fragment: str) -> None:
    """未知牌、同种超过 4 张必须抛 InvalidHandError，并给出可读原因。"""
    with pytest.raises(InvalidHandError, match=fragment):
        validate_hand(hand)


def test_张数校验仅在指定allowed_lengths时生效() -> None:
    """validate_hand 默认不限制张数；传入 allowed_lengths 后 13/14 之外一律拒绝。"""
    thirteen = list(TILE_KINDS[:13])
    fourteen = list(TILE_KINDS[:14])

    validate_hand(thirteen)  # 不限制张数，合法
    validate_hand(thirteen, allowed_lengths=(13, 14))
    validate_hand(fourteen, allowed_lengths=(13, 14))
    with pytest.raises(InvalidHandError, match="张数"):
        validate_hand(thirteen, allowed_lengths=(14,))
    with pytest.raises(InvalidHandError, match="张数"):
        validate_hand([], allowed_lengths=(13, 14))


def test_异常继承关系符合契约() -> None:
    """三个异常都继承 MahjongError；MahjongError 继承 ValueError，便于 API 层映射 400。"""
    assert issubclass(MahjongError, ValueError)
    assert issubclass(InvalidHandError, MahjongError)
    assert issubclass(NotEnoughTilesError, MahjongError)
