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
from app.services.repository.base import GameRepository

__all__ = [
    "SettingsDep",
    "StorageKind",
    "StoreDep",
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

StoreDep = Annotated[GameRepository, Depends(get_store)]
"""对局仓库依赖：``store: StoreDep``；内存 / SQL / Redis 实现由配置决定。"""
