"""FastAPI 依赖注入：配置与对局仓库。

用 ``Depends`` 注入而不是在路由里直接 ``import`` 全局单例，好处：

- 测试可以传入自定义 ``Settings`` / ``GameStore``，用例之间互不污染；
- 路由函数依赖的是「接口」而不是「具体实例的创建时机」，方便后续换 Redis 实现。
"""

from typing import Annotated

from fastapi import Depends, Request

from app.core.config import Settings
from app.services.store import GameStore, get_store

__all__ = ["SettingsDep", "StoreDep", "get_settings", "get_store"]


def get_settings(request: Request) -> Settings:
    """从 ``app.state`` 取出应用配置。"""
    settings = getattr(request.app.state, "settings", None)
    if settings is None:  # pragma: no cover - 只有绕过 create_app 才会触发
        raise RuntimeError("Settings 未初始化：请通过 app.main.create_app() 创建应用")
    return settings


SettingsDep = Annotated[Settings, Depends(get_settings)]
"""配置依赖：``settings: SettingsDep``。"""

StoreDep = Annotated[GameStore, Depends(get_store)]
"""对局仓库依赖：``store: StoreDep``。"""
