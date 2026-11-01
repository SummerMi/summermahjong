"""对局仓库包：抽象接口 + 两种可切换的实现。

- :mod:`~app.services.repository.base`：抽象接口与存储无关的领域操作；
- :mod:`~app.services.repository.sql`：SQLAlchemy 2.0 异步实现（SQLite / MySQL）；
- 内存实现 :class:`app.services.store.GameStore` 放在 ``app/services/store.py``，
  保留原有导入路径（``from app.services.store import GameStore``），向后兼容。
"""

from app.services.repository.base import (
    SEATS,
    ConcurrentUpdateError,
    GameRepository,
    GameState,
    apply_discard,
    new_game_state,
)
from app.services.repository.sql import (
    Base,
    GameRow,
    SqlGameRepository,
    create_engine_from_url,
    init_models,
    mask_dsn,
)

__all__ = [
    "SEATS",
    "Base",
    "ConcurrentUpdateError",
    "GameRepository",
    "GameRow",
    "GameState",
    "SqlGameRepository",
    "apply_discard",
    "create_engine_from_url",
    "init_models",
    "mask_dsn",
    "new_game_state",
]
