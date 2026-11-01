"""广东麻将（推倒胡）领域引擎。

对外只暴露契约（``docs/engineering-contract.md`` 第 4 节）中的接口：

- :mod:`~app.services.mahjong.tiles`：牌面编码、牌墙、洗牌、发牌、手牌工具与异常；
- :mod:`~app.services.mahjong.rules`：胡牌判定（标准型 / 七对）与听牌计算。

本包是纯逻辑，无 I/O、无全局可变状态，API 层可直接同步调用；
若未来耗时上升，可在路由层用 ``run_in_threadpool`` 包装而不必改这里。
"""

from .rules import is_winning_hand, waiting_tiles, winning_pattern
from .tiles import (
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

__all__ = [
    "DECK_SIZE",
    "DEFAULT_SEATS",
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
    "is_winning_hand",
    "shuffle_deck",
    "sort_hand",
    "validate_hand",
    "waiting_tiles",
    "winning_pattern",
]
