"""对局路由：创建对局、查询对局、打牌。

状态存放在 ``app.state.store``（内存 ``GameStore``），通过 ``Depends(get_store)``
注入；路由只做「取状态 → 组装响应」，具体校验与状态迁移都在服务层，
这样以后把内存换成 Redis 时路由不用改。
"""

import logging

from fastapi import APIRouter

from app.api.deps import StoreDep
from app.schemas.mahjong import (
    DiscardRequest,
    DiscardResponse,
    GameCreateRequest,
    GameCreateResponse,
    GameDetailResponse,
)
from app.services.store import SEATS, GameState

logger = logging.getLogger(__name__)

router = APIRouter(tags=["对局"])


def _full_hands(state: GameState) -> dict[str, list[str]]:
    """创建对局时返回四家完整手牌（演示 / 调试用）。"""
    return {seat: list(state.hands[seat]) for seat in SEATS}


def _masked_hands(state: GameState) -> dict[str, list[str] | int]:
    """查询对局时只暴露庄家手牌，其余座位仅给张数，避免“看穿全场”。"""
    return {
        seat: (list(state.hands[seat]) if seat == "east" else len(state.hands[seat]))
        for seat in SEATS
    }


@router.post(
    "",
    response_model=GameCreateResponse,
    summary="创建对局",
    description="洗牌发牌创建一局；传相同 seed 可复现同一牌局。牌墙剩余 83 张。",
)
async def create_game(store: StoreDep, payload: GameCreateRequest | None = None) -> GameCreateResponse:
    """创建一局新对局并返回完整快照。"""
    seed = payload.seed if payload is not None else None
    state = await store.create(seed=seed)
    return GameCreateResponse(
        game_id=state.game_id,
        wall_remaining=len(state.wall),
        current_player=state.current_player,
        hands=_full_hands(state),
        created_at=state.created_at,
    )


@router.get(
    "/{game_id}",
    response_model=GameDetailResponse,
    summary="查询对局",
    description="返回牌局当前状态；hands 中只有 east 给完整手牌，其余座位给张数。",
)
async def get_game(game_id: str, store: StoreDep) -> GameDetailResponse:
    """按 id 查询对局状态。"""
    state = await store.get(game_id)
    return GameDetailResponse(
        game_id=state.game_id,
        wall_remaining=len(state.wall),
        current_player=state.current_player,
        hands=_masked_hands(state),
        created_at=state.created_at,
    )


@router.post(
    "/{game_id}/discard",
    response_model=DiscardResponse,
    summary="打牌",
    description="当前玩家打出一张手牌：牌进牌河、轮转下家、下家自动摸一张。",
)
async def discard(
    game_id: str,
    payload: DiscardRequest,
    store: StoreDep,
) -> DiscardResponse:
    """执行一次打牌并返回牌河与下家信息。"""
    state = await store.discard(game_id, payload.seat, payload.tile)
    return DiscardResponse(
        seat=payload.seat,
        tile=payload.tile,
        wall_remaining=len(state.wall),
        discards={seat: list(state.discards[seat]) for seat in SEATS},
        next_player=state.current_player,
    )
