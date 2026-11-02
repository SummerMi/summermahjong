"""cache-aside 读缓存层测试：fakeredis 注入，不需要真 Redis 进程。

覆盖点（对应 ``docs/adr-0004-cache-aside.md``）：

1. **命中 / 未命中**：第一次回源（miss），第二次走缓存（hit），源调用次数不增加；
2. **空结果不缓存**：源抛 ``GameNotFoundError`` 时不写缓存，连续两次都打到源（防穿透）；
3. **TTL 过期**：很短的 ttl 到期后重新 miss、重新回源；
4. **写后删**：打牌后缓存被删，下一次查询拿到新状态（牌墙递减、牌河 +1）；
5. **建局写穿**：``create`` 预写缓存后立刻查询即命中；
6. **故障降级**：缓存客户端每次操作都抛 ``ConnectionError`` 时，读写仍走源、不抛 500；
7. **统计**：hits / misses / errors / hit_rate 计数正确；
8. **一致性**：缓存命中返回的快照与源直读逐字段相等（含 ``datetime`` 时区）；
9. **缓存关闭**：``ttl_seconds=0`` 时完全直通源，不做任何缓存操作；
10. **脏缓存**：非法 JSON / game_id 不匹配时按未命中处理并覆盖；
11. **前缀隔离**：缓存 key ``cache:game:`` 与 Redis 存储 key ``game:`` 不冲突；
12. **全链路**：``create_app(Settings(redis_url=..., cache_ttl_seconds=30))`` + TestClient，
    测试里手工接线（main.py 的接线由 Lead 负责），跑通建局 → 查询 miss → 再查询 hit →
    打牌 → 查询拿到新值。

约定：与其他测试一致，异步场景用 ``asyncio.run`` 包裹，不引入 pytest-asyncio。
"""

import asyncio
from collections.abc import AsyncIterator
from typing import Any

import fakeredis.aioredis
import pytest
from fastapi.testclient import TestClient
from redis.exceptions import ConnectionError as RedisConnectionError

from app.core.config import Settings
from app.core.errors import GameNotFoundError
from app.main import create_app
from app.services.cache.cache_aside import CACHE_KEY_PREFIX, CachedGameRepository
from app.services.repository import redis as redis_module
from app.services.repository.base import GameRepository, GameState
from app.services.store import GameStore


def _new_fake_client() -> fakeredis.aioredis.FakeRedis:
    """造一个与生产配置一致的 fakeredis 异步客户端（``decode_responses=True``）。"""
    return fakeredis.aioredis.FakeRedis(decode_responses=True)


class _CountingRepository(GameRepository):
    """给任意仓库套一层调用计数，用来证明「命中缓存时没有回源」。"""

    def __init__(self, inner: GameRepository) -> None:
        self.inner = inner
        self.creates = 0
        self.gets = 0
        self.discards = 0
        self.clears = 0

    async def create(self, seed: int | None = None) -> GameState:
        self.creates += 1
        return await self.inner.create(seed=seed)

    async def get(self, game_id: str) -> GameState:
        self.gets += 1
        return await self.inner.get(game_id)

    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        self.discards += 1
        return await self.inner.discard(game_id, seat, tile)

    async def clear(self) -> None:
        self.clears += 1
        await self.inner.clear()


class _BrokenCacheClient:
    """模拟 Redis 挂掉：任何缓存操作都抛 ``ConnectionError``。"""

    def __init__(self) -> None:
        self.calls = 0

    async def get(self, name: str) -> Any:
        self.calls += 1
        raise RedisConnectionError("cache down")

    async def set(self, name: str, value: str, ex: int | None = None) -> Any:
        self.calls += 1
        raise RedisConnectionError("cache down")

    async def delete(self, *names: str) -> Any:
        self.calls += 1
        raise RedisConnectionError("cache down")

    def scan_iter(self, match: str | None = None) -> AsyncIterator[str]:
        async def _gen() -> AsyncIterator[str]:
            self.calls += 1
            raise RedisConnectionError("cache down")
            yield ""  # pragma: no cover - 让函数成为 async generator

        return _gen()


# --------------------------------------------------------------------------- #
# 1. 命中 / 未命中
# --------------------------------------------------------------------------- #


def test_未命中回源后第二次命中且源只读一次() -> None:
    """第一次 miss 回源，第二次 hit 直接返回；源仓库只被读 1 次。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            state = await cached.create(seed=42)
            # create 是 write-through，先删掉缓存模拟「冷缓存」场景
            await client.delete(cached.cache_key(state.game_id))

            first = await cached.get(state.game_id)
            assert source.gets == 1
            second = await cached.get(state.game_id)
            assert source.gets == 1  # 命中缓存，没有再回源
            assert second.hands == first.hands
            assert (cached.hits, cached.misses, cached.errors) == (1, 1, 0)
            assert cached.hit_rate == 0.5
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_缓存命中返回的快照与源直读逐字段一致() -> None:
    """缓存值是 JSON，读回后 wall/hands/discards/current_player/created_at 必须等价。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            state = await cached.create(seed=33)
            await client.delete(cached.cache_key(state.game_id))

            hit = await cached.get(state.game_id)  # miss → 写缓存
            hit = await cached.get(state.game_id)  # hit → 从缓存反序列化
            direct = await source.get(state.game_id)  # 源直读
            assert hit.game_id == direct.game_id
            assert hit.seed == direct.seed == 33
            assert hit.wall == direct.wall
            assert hit.hands == direct.hands
            assert hit.discards == direct.discards
            assert hit.current_player == direct.current_player
            assert hit.version == direct.version
            assert hit.created_at == direct.created_at
            assert hit.created_at.tzinfo is not None  # 缓存读回仍带时区
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 2. 空结果不缓存（防穿透）
# --------------------------------------------------------------------------- #


def test_不存在的对局不缓存空结果() -> None:
    """源抛 ``GameNotFoundError`` 时不写缓存，两次查询都回源（不缓存空值）。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            with pytest.raises(GameNotFoundError):
                await cached.get("no-such-game")
            with pytest.raises(GameNotFoundError):
                await cached.get("no-such-game")

            assert source.gets == 2  # 两次都打到源，没有缓存空结果
            assert (cached.hits, cached.misses, cached.errors) == (0, 2, 0)
            assert await client.exists(cached.cache_key("no-such-game")) == 0
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 3. TTL 过期
# --------------------------------------------------------------------------- #


def test_TTL过期后重新未命中并回源() -> None:
    """``ttl_seconds=1``：写进缓存 1.2 秒后过期，第二次查询重新回源。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=1)
        try:
            state = await cached.create(seed=5)
            await client.delete(cached.cache_key(state.game_id))
            await cached.get(state.game_id)
            assert source.gets == 1
            assert await client.exists(cached.cache_key(state.game_id)) == 1

            await asyncio.sleep(1.2)
            assert await client.exists(cached.cache_key(state.game_id)) == 0
            await cached.get(state.game_id)
            assert source.gets == 2
            assert (cached.hits, cached.misses) == (0, 2)
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 4. 写后删：打牌让缓存失效
# --------------------------------------------------------------------------- #


def test_打牌后缓存失效且下次读到新状态() -> None:
    """discard 先写源再删缓存；下一次 get 回源拿到 wall=82、牌河 +1 的新快照。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            state = await cached.create(seed=11)
            await client.delete(cached.cache_key(state.game_id))
            first = await cached.get(state.game_id)  # miss → 缓存旧状态
            tile = first.hands["east"][0]

            after_discard = await cached.discard(state.game_id, "east", tile)
            assert source.discards == 1
            assert await client.exists(cached.cache_key(state.game_id)) == 0  # 写后删

            refreshed = await cached.get(state.game_id)  # 重新回源
            assert refreshed.wall and len(refreshed.wall) == 82
            assert refreshed.discards["east"] == [tile]
            assert refreshed.current_player == "south"
            assert refreshed.version == after_discard.version  # 源仓库的版本语义（内存实现恒 0）
            assert source.gets == 2
            assert (cached.hits, cached.misses) == (0, 2)

            # 再查一次命中刚写入的新值
            again = await cached.get(state.game_id)
            assert cached.hits == 1
            assert again.hands == after_discard.hands
            assert len(again.wall) == 82
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 5. 建局 write-through
# --------------------------------------------------------------------------- #


def test_建局时写穿缓存_查询直接命中() -> None:
    """``create`` 后缓存里已有该局，紧接着的第一次查询即命中，且缓存带 TTL。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            state = await cached.create(seed=7)
            assert await client.exists(cached.cache_key(state.game_id)) == 1
            ttl = await client.ttl(cached.cache_key(state.game_id))
            assert 0 < ttl <= 30

            hit = await cached.get(state.game_id)
            assert source.gets == 0  # 没有回源
            assert hit.hands == state.hands
            assert (cached.hits, cached.misses) == (1, 0)
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 6. 缓存故障降级
# --------------------------------------------------------------------------- #


def test_缓存故障时自动降级仍能读写源() -> None:
    """缓存客户端每次操作都抛 ``ConnectionError``：业务全部走源，只记 errors 不抛 500。"""

    async def scenario() -> None:
        broken = _BrokenCacheClient()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, broken, ttl_seconds=30)

        state = await cached.create(seed=1)
        assert len(state.wall) == 83
        first = await cached.get(state.game_id)
        assert first.hands == state.hands
        second = await cached.get(state.game_id)
        assert second.wall == first.wall
        tile = first.hands["east"][0]
        after = await cached.discard(state.game_id, "east", tile)
        assert len(after.wall) == 82
        await cached.clear()

        assert (source.creates, source.gets, source.discards, source.clears) == (1, 2, 1, 1)
        assert (cached.hits, cached.misses, cached.errors) == (0, 2, 7)
        assert broken.calls == 7  # 每个缓存操作都失败一次，全部被降级吞掉

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 7. 统计
# --------------------------------------------------------------------------- #


def test_命中率与错误计数正确() -> None:
    """2 次 miss + 2 次 hit → hit_rate=0.5；健康缓存 errors 恒为 0。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            state = await cached.create(seed=2)
            await client.delete(cached.cache_key(state.game_id))
            await cached.get(state.game_id)  # miss
            await cached.get(state.game_id)  # hit
            await cached.get(state.game_id)  # hit
            tile = state.hands["east"][0]
            await cached.discard(state.game_id, "east", tile)
            await cached.get(state.game_id)  # discard 删了缓存 → miss

            assert (cached.hits, cached.misses, cached.errors) == (2, 2, 0)
            assert cached.hit_rate == 0.5
            assert source.gets == 2  # 两次 miss 各回源一次
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 8. 关闭缓存 / 脏缓存 / 前缀隔离
# --------------------------------------------------------------------------- #


def test_缓存关闭时完全直通源() -> None:
    """``ttl_seconds=0``：不读写缓存（坏客户端也不会被调用），行为等同不用缓存。"""

    async def scenario() -> None:
        broken = _BrokenCacheClient()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, broken, ttl_seconds=0)
        assert cached.enabled is False

        state = await cached.create(seed=3)
        fetched = await cached.get(state.game_id)
        assert fetched.hands == state.hands
        after = await cached.discard(state.game_id, "east", fetched.hands["east"][0])
        assert len(after.wall) == 82
        await cached.clear()

        assert broken.calls == 0
        assert cached.errors == 0
        assert source.gets == 1
        assert cached.hit_rate == 0.0
        with pytest.raises(GameNotFoundError):
            await cached.get(state.game_id)

    asyncio.run(scenario())


def test_脏缓存按未命中处理并覆盖() -> None:
    """非法 JSON / game_id 不匹配都当作未命中：删掉脏值、回源、覆盖为合法快照。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            state = await cached.create(seed=3)
            key = cached.cache_key(state.game_id)

            await client.set(key, "not-json")  # 手工写坏
            result = await cached.get(state.game_id)
            assert result.hands == state.hands
            assert (cached.errors, cached.misses, cached.hits) == (1, 1, 0)
            assert await client.exists(key) == 1  # 已被合法值覆盖

            await client.set(key, '{"game_id": "another-game"}')  # 串局
            result = await cached.get(state.game_id)
            assert result.hands == state.hands
            assert cached.errors == 2
            assert source.gets == 2

            assert await client.exists(key) == 1
            await cached.get(state.game_id)  # 命中
            assert (cached.hits, cached.misses) == (1, 2)
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_缓存键与Redis存储键前缀隔离() -> None:
    """缓存 key 用 ``cache:game:``，Redis 存储 key 用 ``game:``，同一实例也不会互相覆盖。"""

    async def scenario() -> None:
        client = _new_fake_client()
        cached = CachedGameRepository(GameStore(), client, ttl_seconds=30)
        try:
            assert CACHE_KEY_PREFIX == "cache:game:"
            assert cached.cache_key("abc") == "cache:game:abc"
            assert redis_module.game_key("abc") == "game:abc"
            assert cached.cache_key("abc") != redis_module.game_key("abc")
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_清空同时清源与缓存且不影响其他前缀() -> None:
    """``clear`` 清源 + 按前缀清缓存；不属于 ``cache:game:`` 的 key 必须保留。"""

    async def scenario() -> None:
        client = _new_fake_client()
        source = _CountingRepository(GameStore())
        cached = CachedGameRepository(source, client, ttl_seconds=30)
        try:
            first = await cached.create(seed=1)
            second = await cached.create(seed=2)
            await client.set("cache:other:key", "keep-me")

            await cached.clear()
            remaining = [key async for key in client.scan_iter(match=f"{CACHE_KEY_PREFIX}*")]
            assert remaining == []
            assert await client.exists("cache:other:key") == 1
            assert source.clears == 1
            assert cached.errors == 0
            with pytest.raises(GameNotFoundError):
                await source.get(first.game_id)
            with pytest.raises(GameNotFoundError):
                await source.get(second.game_id)
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_非法TTL参数被拒绝() -> None:
    """``ttl_seconds < 0`` 是配置错误，构造时直接抛 ``ValueError``（0 合法 = 关缓存）。"""
    with pytest.raises(ValueError):
        CachedGameRepository(GameStore(), _BrokenCacheClient(), ttl_seconds=-1)


# --------------------------------------------------------------------------- #
# 9. 全链路：create_app + TestClient（redis 同时当存储与缓存，测试里手工接线）
# --------------------------------------------------------------------------- #


def test_API闭环_建局查询miss再查询hit打牌后读到新值(monkeypatch: pytest.MonkeyPatch) -> None:
    """HTTP 层零改动：建局 → 查询 miss → 再查询 hit → 打牌删缓存 → 查询拿到新值。"""
    created_clients: list[Any] = []

    def _factory(redis_url: str) -> Any:
        fake = fakeredis.aioredis.FakeRedis(decode_responses=True)
        created_clients.append(fake)
        return fake

    monkeypatch.setattr(redis_module, "create_redis_client", _factory)
    app = create_app(
        Settings(
            redis_url="redis://127.0.0.1:6379/0",
            cache_ttl_seconds=30,
            game_ttl_seconds=3600,
        )
    )

    with TestClient(app) as client:
        # main.py 的缓存接线由 Lead 负责；测试里手工把 source 套上装饰器：
        # 同一个 fakeredis 既当 Redis 存储（game:）又当缓存（cache:game:），前缀隔离。
        storage_client = created_clients[0]
        cached = CachedGameRepository(app.state.store, storage_client, ttl_seconds=30)
        app.state.store = cached

        created = client.post("/api/v1/games", json={"seed": 42}).json()
        game_id = created["game_id"]
        assert created["wall_remaining"] == 83
        assert (cached.hits, cached.misses) == (0, 0)  # create 是 write-through，不算读

        # 模拟「缓存冷启动」：清掉 create 预写的缓存，让第一次查询走 miss 路径。
        # TestClient.portal 是 Starlette 暴露的服务器事件循环，用它安全地 await 异步删 key。
        client.portal.call(storage_client.delete, cached.cache_key(game_id))

        first = client.get(f"/api/v1/games/{game_id}")
        assert first.status_code == 200
        assert (cached.hits, cached.misses) == (0, 1)  # miss → 回源 Redis 存储

        second = client.get(f"/api/v1/games/{game_id}")
        assert second.status_code == 200
        assert (cached.hits, cached.misses) == (1, 1)  # hit
        assert second.json()["hands"]["east"] == first.json()["hands"]["east"]

        tile = created["hands"]["east"][0]
        discarded = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": tile}
        )
        assert discarded.status_code == 200
        assert discarded.json()["wall_remaining"] == 82

        after = client.get(f"/api/v1/games/{game_id}")
        assert after.status_code == 200
        assert (cached.hits, cached.misses) == (1, 2)  # discard 删缓存 → 重新 miss
        assert after.json()["wall_remaining"] == 82
        assert after.json()["current_player"] == "south"
        assert after.json()["hands"]["south"] == 14

        again = client.get(f"/api/v1/games/{game_id}")
        assert again.status_code == 200
        assert (cached.hits, cached.misses) == (2, 2)  # 新值已缓存，命中
