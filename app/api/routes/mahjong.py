"""麻将规则路由：牌墙清单、胡牌判定、听牌计算。

引擎（``app.services.mahjong``）是纯 CPU 纯函数，这里**同步调用**：
单次判定是毫秒级、无 I/O，为了它套 ``run_in_threadpool`` 反而增加线程切换开销。
真要做 CPU 密集扩展时，应该把引擎拆成独立 worker 或用多进程，这一点面试可讲。
"""

import logging

from fastapi import APIRouter

from app.core.errors import InvalidHandError
from app.schemas.mahjong import (
    TenpaiRequest,
    TenpaiResponse,
    TilesResponse,
    WinCheckRequest,
    WinCheckResponse,
)
from app.services.mahjong.rules import waiting_tiles, winning_pattern
from app.services.mahjong.tiles import (
    TILE_KINDS,
    build_deck,
    validate_hand,
)
from app.services.mahjong.tiles import (
    InvalidHandError as EngineInvalidHandError,
)

logger = logging.getLogger(__name__)

router = APIRouter(tags=["麻将规则"])


def _ensure_hand_valid(hand: list[str], allowed_lengths: tuple[int, ...]) -> None:
    """业务层手牌校验：张数、未知牌、同种牌超过 4 张。

    校验规则本身交给引擎的 ``validate_hand``（领域内唯一真相），这里只负责
    把领域异常翻译成 API 错误：走统一 400 信封，而不是 FastAPI 默认的 422 结构。
    因此 schema 只判断“是不是字符串数组”这种格式问题。
    """
    try:
        validate_hand(hand, allowed_lengths=allowed_lengths)
    except EngineInvalidHandError as exc:
        raise InvalidHandError(str(exc)) from exc


@router.get(
    "/tiles",
    response_model=TilesResponse,
    summary="全副牌信息",
    description="返回总张数、牌型种数与 34 种牌型清单（顺序固定，供前端渲染牌桌）。",
)
async def list_tiles() -> TilesResponse:
    """返回全副牌静态信息。"""
    return TilesResponse(total=len(build_deck()), kinds=len(TILE_KINDS), tiles=list(TILE_KINDS))


@router.post(
    "/win-check",
    response_model=WinCheckResponse,
    summary="胡牌判定",
    description="14 张手牌按「4 面子 + 1 将 / 七对」判定；13 张表示尚未摸牌，直接判未胡。",
)
async def check_win(payload: WinCheckRequest) -> WinCheckResponse:
    """判定手牌是否胡牌。"""
    hand = payload.hand
    _ensure_hand_valid(hand, (13, 14))

    # 13 张尚未摸牌（少一张将牌），不可能是完整胡牌型，直接返回未胡，
    # 这样前端轮询同一接口时不必区分“13 张”还是“14 张”。
    if len(hand) == 13:
        return WinCheckResponse(is_win=False, pattern=None)

    try:
        pattern = winning_pattern(hand)
    except EngineInvalidHandError as exc:  # 引擎的兜底校验，转成统一 400 信封
        raise InvalidHandError(str(exc)) from exc

    return WinCheckResponse(is_win=pattern is not None, pattern=pattern)


@router.post(
    "/tenpai",
    response_model=TenpaiResponse,
    summary="听牌计算",
    description="13 张手牌 → 所有能胡的牌种列表（升序）；空列表表示不听牌。",
)
async def calc_tenpai(payload: TenpaiRequest) -> TenpaiResponse:
    """计算 13 张手牌的听牌列表。"""
    hand = payload.hand
    _ensure_hand_valid(hand, (13,))

    try:
        tiles = waiting_tiles(hand)
    except EngineInvalidHandError as exc:
        raise InvalidHandError(str(exc)) from exc

    return TenpaiResponse(waiting_tiles=tiles)
