"""对局路由：创建对局、查询对局、打牌（含座位绑定）。

状态存放在 ``app.state.store``（``GameRepository`` 抽象），通过 ``Depends(get_store)``
注入；路由只做「鉴权 → 取状态 → 组装响应」，校验与状态迁移在服务层。

**座位绑定**（``JWT_SECRET`` 非空时生效）：

- 建局 / 查询 / 打牌都必须带合法座位令牌；
- 建局与查询只返回「令牌座位自己」的手牌，其余座位只给张数；
- 打牌时 ``body.seat`` 必须等于令牌里的座位，否则 403 ``SEAT_MISMATCH``
  （水平越权防护：不能替别人出牌）。

**鉴权关闭时**（``JWT_SECRET`` 为空，demo / CI 默认）``seat`` 为 ``None``，
走的是改造前的老行为：建局给四家完整手牌、查询只暴露庄家、打牌不校验身份，
保证已有调用方与测试零改动。
"""

import logging

from fastapi import APIRouter

from app.api.deps import CurrentSeat, StoreDep
from app.core.errors import SeatMismatchError
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


def _full_hands(state: GameState, seat: str | None) -> dict[str, list[str] | int]:
    """建局响应里的 ``hands``。

    - 鉴权关闭（``seat is None``）：四家完整手牌，保持老接口行为；
    - 鉴权开启：只给 ``seat`` 自己的手牌，其余座位给张数（防止看穿全场）。
    """
    if seat is None:
        return {current: list(state.hands[current]) for current in SEATS}
    return {
        current: (list(state.hands[current]) if current == seat else len(state.hands[current]))
        for current in SEATS
    }


def _masked_hands(state: GameState, seat: str | None) -> dict[str, list[str] | int]:
    """查询响应里的 ``hands``。

    - 鉴权关闭：沿用老行为，只暴露庄家（east）手牌；
    - 鉴权开启：只暴露令牌座位自己的手牌，其余座位给张数。
    """
    visible_seat = seat or "east"
    return {
        current: (
            list(state.hands[current]) if current == visible_seat else len(state.hands[current])
        )
        for current in SEATS
    }


@router.post(
    "",
    response_model=GameCreateResponse,
    summary="创建对局",
    description=(
        "洗牌发牌创建一局；传相同 seed 可复现同一牌局，牌墙剩余 83 张。"
        "开启鉴权后必须带座位令牌，且只返回自己座位的手牌。"
    ),
)
async def create_game(
    store: StoreDep,
    seat: CurrentSeat,
    payload: GameCreateRequest | None = None,
) -> GameCreateResponse:
    """创建一局新对局并返回牌局快照（鉴权开启时手牌按座位遮罩）。"""
    seed = payload.seed if payload is not None else None
    state = await store.create(seed=seed)
    return GameCreateResponse(
        game_id=state.game_id,
        wall_remaining=len(state.wall),
        current_player=state.current_player,
        hands=_full_hands(state, seat),
        created_at=state.created_at,
    )


@router.get(
    "/{game_id}",
    response_model=GameDetailResponse,
    summary="查询对局",
    description=(
        "返回牌局当前状态；hands 中只给令牌座位自己的完整手牌，其余座位给张数。"
        "鉴权关闭时沿用旧行为（只暴露庄家手牌）。"
    ),
)
async def get_game(game_id: str, store: StoreDep, seat: CurrentSeat) -> GameDetailResponse:
    """按 id 查询对局状态。"""
    state = await store.get(game_id)
    return GameDetailResponse(
        game_id=state.game_id,
        wall_remaining=len(state.wall),
        current_player=state.current_player,
        hands=_masked_hands(state, seat),
        created_at=state.created_at,
    )


@router.post(
    "/{game_id}/discard",
    response_model=DiscardResponse,
    summary="打牌",
    description=(
        "当前玩家打出一张手牌：牌进牌河、轮转下家、下家自动摸一张。"
        "开启鉴权时 body.seat 必须与令牌座位一致，否则 403 SEAT_MISMATCH。"
    ),
)
async def discard(
    game_id: str,
    payload: DiscardRequest,
    store: StoreDep,
    seat: CurrentSeat,
) -> DiscardResponse:
    """执行一次打牌并返回牌河与下家信息。"""
    # 座位绑定：令牌说你是谁，就只能以谁的身份出牌（水平越权防护）。
    # 先做「身份 → 座位」校验再去查对局：鉴权/授权失败优先返回 403，
    # 不向调用方泄露「这个 game_id 是否存在」（避免被枚举）。
    if seat is not None and payload.seat != seat:
        raise SeatMismatchError(token_seat=seat, body_seat=payload.seat)

    state = await store.discard(game_id, payload.seat, payload.tile)
    return DiscardResponse(
        seat=payload.seat,
        tile=payload.tile,
        wall_remaining=len(state.wall),
        discards={current: list(state.discards[current]) for current in SEATS},
        next_player=state.current_player,
    )
