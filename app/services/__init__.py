"""服务层：内存对局仓库 + 麻将规则引擎（engine 由工程 A 提供）。"""

from app.services.store import (
    SEATS,
    GameState,
    GameStore,
    get_store,
)

__all__ = ["SEATS", "GameState", "GameStore", "get_store"]
