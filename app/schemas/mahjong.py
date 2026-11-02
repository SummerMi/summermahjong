"""麻将相关的请求 / 响应模型。

分层原则：

- 这里只做**格式**校验：字段类型、必填、座位枚举、数组结构。
- 手牌合法性（张数、未知牌、同种牌超过 4 张）与规则判定（能否胡、能否打出）
  属于**业务**校验，放在路由 / 服务层，由引擎抛出的 ``InvalidHandError``
  统一转成 400 信封。这样 schema 不会和规则引擎出现两套“真相”。
"""

from datetime import datetime
from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

Seat = Literal["east", "south", "west", "north"]
"""座位枚举；east 为庄家（多一张牌）。"""


class TilesResponse(BaseModel):
    """全副牌的静态信息，前端用来渲染牌桌 / 校验输入。"""

    total: int = Field(..., description="全副牌总张数（34 种 × 4 张）", examples=[136])
    kinds: int = Field(..., description="牌型种数", examples=[34])
    tiles: list[str] = Field(
        ...,
        description="34 种牌型清单，顺序固定为 m → p → s → E S W N P F C",
        examples=[["1m", "2m", "3m", "4m", "5m", "6m", "7m"]],
    )


class WinCheckRequest(BaseModel):
    """胡牌判定请求。14 张按规则判定，13 张直接判未胡。"""

    model_config = ConfigDict(
        json_schema_extra={
            "examples": [
                {
                    "hand": [
                        "1m", "2m", "3m", "4m", "5m", "6m", "7m",
                        "8m", "9m", "1p", "1p", "1p", "E", "E",
                    ]
                }
            ]
        }
    )

    hand: list[str] = Field(
        ...,
        description="手牌数组，元素为牌面编码（如 3m / 5p / E）；张数由业务层校验",
        examples=[["1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "1p", "E", "E"]],
    )


class WinCheckResponse(BaseModel):
    """胡牌判定结果。"""

    is_win: bool = Field(..., description="是否胡牌", examples=[True])
    pattern: Literal["standard", "seven_pairs"] | None = Field(
        None,
        description="胡牌牌型：standard=4 面子 + 1 将，seven_pairs=七对；未胡为 null",
        examples=["standard"],
    )


class TenpaiRequest(BaseModel):
    """听牌计算请求：必须正好 13 张手牌。"""

    model_config = ConfigDict(
        json_schema_extra={
            "examples": [
                {
                    "hand": [
                        "1m", "2m", "3m", "4m", "5m", "6m", "7m",
                        "8m", "9m", "1p", "1p", "1p", "E",
                    ]
                }
            ]
        }
    )

    hand: list[str] = Field(
        ...,
        description="13 张手牌；张数由业务层校验",
        examples=[["1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "1p", "E"]],
    )


class TenpaiResponse(BaseModel):
    """听牌计算结果。"""

    waiting_tiles: list[str] = Field(
        ...,
        description="能胡的牌种列表（升序）；空列表表示不听牌",
        examples=[["E"]],
    )


class GameCreateRequest(BaseModel):
    """创建对局请求。``seed`` 可省略，相同 seed 可复现同一副牌。"""

    model_config = ConfigDict(json_schema_extra={"examples": [{"seed": 42}]})

    seed: int | None = Field(
        None,
        description="随机种子；传相同 seed 可复现同一牌局，省略则每次随机",
        examples=[42],
    )


class GameCreateResponse(BaseModel):
    """创建对局成功后的牌局快照。

    ``hands`` 的类型是「手牌数组或张数」：

    - 鉴权关闭（默认）：四家手牌都给完整数组，便于本地演示与调试；
    - 鉴权开启：只给令牌座位自己的完整手牌，其余座位给张数，防止“看穿全场”。
    """

    game_id: str = Field(..., description="对局 id（uuid4().hex）", examples=["9f2c1d..."])
    wall_remaining: int = Field(..., description="牌墙剩余张数（发完牌后为 83）", examples=[83])
    current_player: Seat = Field(..., description="当前该谁出牌", examples=["east"])
    hands: dict[str, list[str] | int] = Field(
        ...,
        description="各家手牌：完整数组表示可见手牌，整数表示该座位手牌张数（被遮蔽）",
        examples=[{"east": ["1m", "2m"], "south": 13, "west": 13, "north": 13}],
    )
    created_at: datetime = Field(..., description="对局创建时间（UTC，ISO 8601）")


class GameDetailResponse(BaseModel):
    """查询对局：只暴露庄家手牌，其余座位只给张数，防止“看穿全场”。"""

    game_id: str = Field(..., description="对局 id", examples=["9f2c1d..."])
    wall_remaining: int = Field(..., description="牌墙剩余张数", examples=[82])
    current_player: Seat = Field(..., description="当前该谁出牌", examples=["south"])
    hands: dict[str, list[str] | int] = Field(
        ...,
        description="east 给完整手牌数组；其余座位给剩余张数（整数）",
        examples=[{"east": ["1m", "2m"], "south": 14, "west": 13, "north": 13}],
    )
    created_at: datetime = Field(..., description="对局创建时间（UTC，ISO 8601）")


class DiscardRequest(BaseModel):
    """打牌请求。"""

    model_config = ConfigDict(json_schema_extra={"examples": [{"seat": "east", "tile": "3m"}]})

    seat: Seat = Field(..., description="打牌座位，必须是当前轮到的玩家", examples=["east"])
    tile: str = Field(..., description="要打出的牌面编码，例如 3m / 5p / E", examples=["3m"])


class DiscardResponse(BaseModel):
    """打牌结果：牌进牌河、轮转下家、下家自动摸一张。"""

    seat: Seat = Field(..., description="谁打出的牌", examples=["east"])
    tile: str = Field(..., description="打出的牌", examples=["3m"])
    wall_remaining: int = Field(..., description="下家摸牌后牌墙剩余张数", examples=[82])
    discards: dict[str, list[str]] = Field(
        ...,
        description="四家牌河（按座位分组）",
        examples=[{"east": ["3m"], "south": [], "west": [], "north": []}],
    )
    next_player: Seat = Field(..., description="下一位出牌者（已自动摸牌）", examples=["south"])
