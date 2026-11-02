"""鉴权路由：签发座位令牌（demo 用，生产应由登录系统签发）。

为什么单独放一个 ``POST /auth/token`` 而不是让前端自己用密钥签：
前端持有密钥等于把签名能力公开，任何人都能签任意座位。这里由服务端签发，
密钥只存在于服务端环境变量；真正的项目里这个接口应改成「账号密码 / 微信登录 →
校验身份 → 绑定座位 → 签发令牌」，本 demo 省略账号体系，直接用座位换 token。
"""

from fastapi import APIRouter
from pydantic import BaseModel, ConfigDict, Field

from app.api.deps import SettingsDep
from app.core.errors import AuthDisabledError
from app.core.security import create_access_token
from app.schemas.mahjong import Seat

router = APIRouter(tags=["鉴权"])


class TokenRequest(BaseModel):
    """换取令牌的请求体：只需要声明你要坐哪个座位。"""

    model_config = ConfigDict(json_schema_extra={"examples": [{"seat": "east"}]})

    seat: Seat = Field(..., description="要绑定的座位（east / south / west / north）", examples=["east"])


class TokenResponse(BaseModel):
    """令牌响应：标准 OAuth2 Bearer 风格字段。"""

    access_token: str = Field(..., description="JWT 字符串，放在 Authorization: Bearer 后面")
    token_type: str = Field(default="bearer", description="令牌类型，固定为 bearer", examples=["bearer"])
    expires_in: int = Field(
        ...,
        description="有效期（秒），由 JWT_EXPIRE_MINUTES 决定",
        examples=[43200],
    )


@router.post(
    "/token",
    response_model=TokenResponse,
    summary="签发座位令牌",
    description=(
        "用声明座位换取 JWT（demo 简化版登录）。鉴权关闭时（JWT_SECRET 为空）"
        "返回 503 AUTH_DISABLED，明确告诉调用方本实例不需要令牌。"
    ),
)
async def issue_token(payload: TokenRequest, settings: SettingsDep) -> TokenResponse:
    """签发一枚绑定座位的访问令牌。

    :raises AuthDisabledError: 本实例未配置 ``JWT_SECRET``，鉴权关闭。
    """
    if not settings.jwt_secret:
        raise AuthDisabledError()

    token = create_access_token(payload.seat, settings)
    return TokenResponse(
        access_token=token,
        token_type="bearer",
        expires_in=settings.jwt_expire_minutes * 60,
    )
