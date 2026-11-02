"""JWT 鉴权：签发与校验座位令牌（纯函数、无 I/O）。

设计要点（面试可讲）：

1. **无状态**：令牌自带座位（``sub``）、签发时间（``iat``）、过期时间（``exp``），
   服务端不存 session，横向扩容时任意实例都能独立校验，不需要共享会话存储。
2. **HS256 对称签名**：单密钥适合小团队 / demo；多服务或第三方接入应换成
   RS256/ES256（私钥签发、公钥验证），密钥管理与轮换成本更低。
3. **座位绑定**：``sub`` 就是座位名，另用 ``type="seat"`` 标记用途，避免把将来
   可能新增的 refresh token 误当座位令牌使用；打牌时再校验
   ``body.seat == token.sub``，防止替别人出牌（水平越权）。
4. **为什么用 Bearer 头而不是 Cookie**：浏览器不会自动携带自定义头，天然规避
   CSRF；代价是 XSS 能偷 token、且无状态 token 无法立即注销，生产一般是
   「短过期 + refresh token + 黑名单/版本号」组合。

密钥为空 = 关闭鉴权（demo/CI 默认）：本模块的函数只在 ``jwt_secret`` 非空时被调用，
此时直接抛 :class:`~app.core.errors.AuthDisabledError`，保证「没开鉴权就别发 token」。
"""

from __future__ import annotations

import logging
from datetime import UTC, datetime, timedelta

import jwt

from app.core.config import Settings
from app.core.errors import AuthDisabledError, TokenExpiredError, TokenInvalidError

logger = logging.getLogger(__name__)

TOKEN_TYPE = "seat"
"""令牌用途标记；未来若有刷新令牌，可用 ``type="refresh"`` 区分。"""


def create_access_token(seat: str, settings: Settings) -> str:
    """为某个座位签发 JWT。

    :param seat: 座位名（east / south / west / north），作为 ``sub`` 写进 claims。
    :param settings: 应用配置，使用其中的 ``jwt_secret`` / ``jwt_algorithm`` /
        ``jwt_expire_minutes``。
    :return: 编码后的 JWT 字符串（``Bearer xxx`` 里的 xxx）。
    :raises AuthDisabledError: 未配置 ``jwt_secret``（鉴权已关闭）。
    """
    if not settings.jwt_secret:
        raise AuthDisabledError()

    now = datetime.now(UTC)
    payload = {
        "sub": seat,  # 主体：座位名，服务端据此做座位绑定
        "type": TOKEN_TYPE,  # 用途：座位令牌（区分未来的 refresh token）
        "iat": now,  # 签发时间
        "exp": now + timedelta(minutes=settings.jwt_expire_minutes),  # 过期时间
    }
    return jwt.encode(payload, settings.jwt_secret, algorithm=settings.jwt_algorithm)


def decode_access_token(token: str, settings: Settings) -> str:
    """校验 JWT 并取出座位名。

    校验内容：签名算法与密钥、``exp`` / ``iat`` / ``sub`` 是否齐全、是否过期、
    用途是否为座位令牌。

    :param token: ``Authorization: Bearer`` 后面的令牌字符串。
    :param settings: 应用配置（密钥 / 算法）。
    :return: 令牌绑定的座位名。
    :raises TokenExpiredError: 令牌已过期（``exp`` 早于当前时间）。
    :raises TokenInvalidError: 签名错误、格式错误、缺少字段、用途不是座位令牌。
    :raises AuthDisabledError: 未配置 ``jwt_secret``（鉴权已关闭）。
    """
    if not settings.jwt_secret:
        raise AuthDisabledError()

    try:
        payload = jwt.decode(
            token,
            settings.jwt_secret,
            algorithms=[settings.jwt_algorithm],
            # 强制要求这三项，防止无过期时间的令牌被当成永久有效
            options={"require": ["exp", "iat", "sub"]},
        )
    except jwt.ExpiredSignatureError as exc:
        raise TokenExpiredError() from exc
    except jwt.InvalidTokenError as exc:
        # 覆盖格式错误（DecodeError）、签名错误（InvalidSignatureError）、
        # 算法不匹配等所有“令牌不可信”的情况，统一按无效处理
        raise TokenInvalidError(f"令牌签名或格式非法（{exc}）") from exc

    token_type = payload.get("type")
    if token_type != TOKEN_TYPE:
        raise TokenInvalidError(f"令牌用途不是座位令牌（期望 type={TOKEN_TYPE}）")

    seat = payload.get("sub")
    if not isinstance(seat, str) or not seat:
        raise TokenInvalidError("令牌缺少有效的 sub（座位）")

    return seat
