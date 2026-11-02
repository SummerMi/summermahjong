"""对局读缓存（cache-aside）包：给任意 ``GameRepository`` 加一层 Redis 读缓存。

- :mod:`app.services.cache.cache_aside`：:class:`CachedGameRepository` 装饰器，
  读走缓存（未命中回源写回）、写（打牌）后删缓存、缓存故障自动降级；
- 与「Redis 当存储」的 :mod:`app.services.repository.redis` 是两种不同角色：
  缓存不是真相，丢了可以从源仓库重新加载。
"""

from app.services.cache.cache_aside import (
    CACHE_KEY_PREFIX,
    AsyncCacheClient,
    CachedGameRepository,
)

__all__ = [
    "CACHE_KEY_PREFIX",
    "AsyncCacheClient",
    "CachedGameRepository",
]
