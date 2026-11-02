"""FastAPI 依赖注入：配置与对局仓库。

用 ``Depends`` 注入而不是在路由里直接 ``import`` 全局单例，好处：

- 测试可以传入自定义 ``Settings`` / ``GameRepository``，用例之间互不污染；
- 路由函数依赖的是「接口」而不是「具体实例的创建时机」，方便后续换 Redis 实现。

三种存储实现的优先级（见 :func:`select_storage`）：

1. ``REDIS_URL`` 非空 → :class:`~app.services.repository.redis.RedisGameRepository`
   （跨进程共享、带 TTL / LRU，适合多实例）；
2. 否则 ``DATABASE_URL`` 非空 → :class:`~app.services.repository.sql.SqlGameRepository`；
3. 都为空 → 内存 :class:`~app.services.store.GameStore`（默认，零依赖）。

具体实例由 ``lifespan`` 按这个规则创建并挂到 ``app.state.store``，路由只通过
:data:`StoreDep` 拿到 ``GameRepository`` 抽象，对实现零感知（依赖倒置）。
"""

from typing import Annotated, Literal

from fastapi import Depends, Request

from app.core.config import Settings
from app.core.errors import TokenInvalidError, TokenMissingError
from app.core.security import decode_access_token
from app.services.repository.base import SEATS, GameRepository

__all__ = [
    "CurrentSeat",
    "SettingsDep",
    "StorageKind",
    "StoreDep",
    "get_current_seat",
    "get_settings",
    "get_store",
    "select_storage",
]

StorageKind = Literal["redis", "sql", "memory"]
"""存储实现的种类；声明顺序即优先级（redis > sql > memory）。"""


def select_storage(settings: Settings) -> StorageKind:
    """按配置选择对局仓库实现：Redis 优先，其次 SQL，都未配置则用内存。

    为什么是「优先级」而不是「互斥校验」：Redis 更适合当多实例共享的对局仓库，
    只要配了 ``REDIS_URL`` 就说明想要它；``DATABASE_URL`` 留给「只想落库」的场景。
    两者都配置时启动日志会明确打印实际选中的实现，避免「以为在用 Redis」的误判。

    :param settings: 应用配置。
    :return: ``"redis"`` / ``"sql"`` / ``"memory"``。
    """
    if settings.redis_url:
        return "redis"
    if settings.database_url:
        return "sql"
    return "memory"


def get_settings(request: Request) -> Settings:
    """从 ``app.state`` 取出应用配置。"""
    settings = getattr(request.app.state, "settings", None)
    if settings is None:  # pragma: no cover - 只有绕过 create_app 才会触发
        raise RuntimeError("Settings 未初始化：请通过 app.main.create_app() 创建应用")
    return settings


def get_store(request: Request) -> GameRepository:
    """FastAPI 依赖：返回当前配置对应的对局仓库实现。

    ``lifespan`` 启动时已按 :func:`select_storage` 选定实现并挂到 ``app.state.store``；
    这里只负责取出。路由因此只依赖 ``GameRepository`` 抽象，对「内存 / SQL / Redis」
    零感知——这就是依赖倒置在真实项目里的样子。

    :raises RuntimeError: 没走 lifespan（例如绕过 ``create_app`` 直接构造请求）。
    """
    store = getattr(request.app.state, "store", None)
    if store is None:  # pragma: no cover - 只有绕过 lifespan 启动才会触发
        raise RuntimeError("GameRepository 未初始化：请确认应用通过了 lifespan 启动流程")
    return store


SettingsDep = Annotated[Settings, Depends(get_settings)]
"""配置依赖：``settings: SettingsDep``。"""


def get_current_seat(request: Request, settings: SettingsDep) -> str | None:
    """FastAPI 依赖：从 ``Authorization: Bearer <token>`` 解析出当前座位。

    两种模式（由 ``JWT_SECRET`` 决定）：

    - **鉴权关闭**（``jwt_secret`` 为空，demo / CI 默认）：直接返回 ``None``，
      不读请求头也不报错，所以老接口的行为与引入 JWT 之前完全一致；
    - **鉴权开启**：必须带合法座位令牌，缺失 / 格式错 / 无效 / 过期分别抛
      401 ``TOKEN_MISSING`` / ``TOKEN_INVALID`` / ``TOKEN_INVALID`` / ``TOKEN_EXPIRED``。

    座位名再回查一次 :data:`SEATS`：令牌签名正确不代表 ``sub`` 合法（例如用同一密钥
    签了别的用途令牌），宁可 401 也不要带着非法座位进入业务逻辑。

    :raises TokenMissingError: 没带 Authorization 头。
    :raises TokenInvalidError: 头格式不是 Bearer、签名/格式错误、座位非法。
    :raises TokenExpiredError: 令牌过期。
    """
    if not settings.jwt_secret:
        # 鉴权关闭：不校验、不报错，返回 None 让路由走「老行为」分支
        return None

    authorization = request.headers.get("Authorization")
    if not authorization:
        raise TokenMissingError()

    scheme, _, token = authorization.partition(" ")
    token = token.strip()
    if scheme.lower() != "bearer" or not token:
        raise TokenInvalidError("Authorization 头格式必须是 Bearer <token>")

    seat = decode_access_token(token, settings)
    if seat not in SEATS:
        raise TokenInvalidError(f"令牌中的座位 {seat!r} 不是合法座位")
    return seat


CurrentSeat = Annotated[str | None, Depends(get_current_seat)]
"""当前请求的座位依赖：鉴权关闭时为 ``None``，开启时由令牌的 ``sub`` 决定。"""

StoreDep = Annotated[GameRepository, Depends(get_store)]
"""对局仓库依赖：``store: StoreDep``；内存 / SQL / Redis 实现由配置决定。"""
