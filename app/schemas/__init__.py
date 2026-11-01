"""Pydantic 请求 / 响应模型。

约定：schema 只负责「格式」层（字段类型、必填性、枚举、示例），
「业务」层（手牌张数是否 13/14、牌面是否合法、能否成胡、能否打出）
放在路由 / 服务层，由业务异常统一转成 400 信封。
"""

from app.schemas.calc import CalcOp, CalcRequest, CalcResponse
from app.schemas.mahjong import (
    DiscardRequest,
    DiscardResponse,
    GameCreateRequest,
    GameCreateResponse,
    GameDetailResponse,
    TenpaiRequest,
    TenpaiResponse,
    TilesResponse,
    WinCheckRequest,
    WinCheckResponse,
)

__all__ = [
    "CalcOp",
    "CalcRequest",
    "CalcResponse",
    "DiscardRequest",
    "DiscardResponse",
    "GameCreateRequest",
    "GameCreateResponse",
    "GameDetailResponse",
    "TenpaiRequest",
    "TenpaiResponse",
    "TilesResponse",
    "WinCheckRequest",
    "WinCheckResponse",
]
