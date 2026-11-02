"""Redis 对局仓库测试：用 ``fakeredis`` 注入，不需要真 Redis 进程。

覆盖点（对应 ``docs/adr-0003-redis存储与缓存.md``）：

1. **配置优先级**：``REDIS_URL`` > ``DATABASE_URL`` > 内存（默认行为完全不变）；
2. **基本读写**：create / get / discard / clear 全流程，以及 404 / 400 错误语义；
3. **三种实现一致性**：同 seed 在内存 / SQL(SQLite) / Redis 下发出同一副牌，
   并且相同的打牌序列得到完全相同的新快照（换存储不换行为）；
4. **TTL**：``EXPIRE`` 到期后按 id 查不到（404 语义）；
5. **上限淘汰**：``max_games=2`` 时建第 3 局淘汰最久未访问的一局（ZSET score = last_access）；
6. **并发**：``WATCH``/``MULTI`` 乐观锁下 15 个并发打牌只有一次成功，
   牌河只多 1 张、总牌数守恒 136；
7. **全链路**：``create_app(settings)`` + ``TestClient`` 注入 fakeredis，
   走真实 HTTP 建局 / 查询 / 打牌 / 错误路径。

约定：与项目其他测试一致，用 ``asyncio.run`` 在同步测试里跑协程，不引入 pytest-asyncio；
``fakeredis`` 只出现在测试里，生产代码依赖的是 ``redis.asyncio.Redis``。
"""

import asyncio
import time
from typing import Any

import fakeredis.aioredis
import pytest
from fastapi.testclient import TestClient

from app.api.deps import select_storage
from app.core.config import Settings
from app.core.errors import AppError, GameNotFoundError, InvalidHandError, TileNotInHandError
from app.main import create_app
from app.services.mahjong.tiles import TILE_KINDS
from app.services.repository import redis as redis_module
from app.services.repository.base import (
    SEATS,
    ConcurrentUpdateError,
    GameRepository,
    GameState,
)
from app.services.repository.redis import (
    INDEX_KEY,
    RedisGameRepository,
    create_redis_client,
    game_key,
)
from app.services.repository.sql import (
    SqlGameRepository,
    create_engine_from_url,
    init_models,
)
from app.services.store import GameStore


def _new_fake_client() -> fakeredis.aioredis.FakeRedis:
    """造一个与 ``create_redis_client`` 配置一致的 fakeredis 异步客户端。"""
    return fakeredis.aioredis.FakeRedis(decode_responses=True)


async def _open_redis_repo(**kwargs: Any) -> tuple[RedisGameRepository, Any]:
    """建 fakeredis 客户端 + Redis 仓库，返回 ``(repository, client)``。"""
    client = _new_fake_client()
    return RedisGameRepository(client, **kwargs), client


async def _open_sql_repo() -> tuple[SqlGameRepository, Any]:
    """建一个 SQLite 内存库的 SQL 仓库（三种实现一致性对照用）。"""
    engine = create_engine_from_url("sqlite+aiosqlite:///:memory:")
    await init_models(engine)
    return SqlGameRepository(engine), engine


def _total_tiles(state: GameState) -> int:
    """总牌数守恒校验：牌墙 + 四家手牌 + 四家牌河 == 136。"""
    return (
        len(state.wall)
        + sum(len(hand) for hand in state.hands.values())
        + sum(len(pile) for pile in state.discards.values())
    )


def _fingerprint(state: GameState) -> tuple[Any, ...]:
    """用于跨实现比对的快照指纹（不含 last_access / version 这类存储相关字段）。"""
    return (state.wall, state.hands, state.discards, state.current_player)


class _SlowRedisGameRepository(RedisGameRepository):
    """测试用：在 ``WATCH`` 读完后主动让出事件循环，把并发窗口放大到必定重叠。

    真实网络下「读」和 ``EXEC`` 之间天然有 IO 间隔；fakeredis 是纯内存、
    await 可能不真正挂起，所以这里显式 ``asyncio.sleep(0)`` 制造同样的交错。
    """

    async def _read_watched(self, pipe: Any, key: str) -> dict[str, Any]:
        data = await super()._read_watched(pipe, key)
        await asyncio.sleep(0)
        return data


# --------------------------------------------------------------------------- #
# 1. 配置优先级：Redis > SQL > 内存（默认行为不变）
# --------------------------------------------------------------------------- #


def test_配置优先级_Redis高于SQL且都为空时内存() -> None:
    """``select_storage``：redis_url 非空则 Redis；否则 database_url；都空则内存。"""
    memory = Settings(redis_url="", database_url="")
    sql = Settings(redis_url="", database_url="sqlite+aiosqlite:///:memory:")
    both = Settings(
        redis_url="redis://127.0.0.1:6379/0",
        database_url="sqlite+aiosqlite:///:memory:",
    )
    assert select_storage(memory) == "memory"
    assert select_storage(sql) == "sql"
    assert select_storage(both) == "redis"


def test_Redis仓库与工厂符合依赖倒置约定() -> None:
    """Redis 实现是 ``GameRepository`` 子类；工厂返回解码字符串的异步客户端。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo()
        try:
            assert isinstance(repo, GameRepository)
        finally:
            await client.aclose()

        real_client = create_redis_client("redis://127.0.0.1:6379/0")
        try:
            assert real_client.connection_pool.connection_kwargs["decode_responses"] is True
        finally:
            await real_client.aclose()

    assert issubclass(RedisGameRepository, GameRepository)
    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 2. 基本读写：create / get / discard / clear
# --------------------------------------------------------------------------- #


def test_Redis仓库完成创建查询打牌清空全流程() -> None:
    """create → get → discard → clear；状态字段与内存 / SQL 实现语义一致。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo()
        try:
            created = await repo.create(seed=42)
            assert created.wall and len(created.wall) == 83
            assert created.current_player == "east"
            assert len(created.hands["east"]) == 14
            assert created.version == 0
            # 存储侧：Hash 存在、EXPIRE 生效、索引里有这一局
            assert await client.exists(game_key(created.game_id)) == 1
            ttl = await client.ttl(game_key(created.game_id))
            assert 0 < ttl <= repo.ttl_seconds
            assert await client.zscore(INDEX_KEY, created.game_id) is not None

            fetched = await repo.get(created.game_id)
            assert fetched.hands == created.hands
            assert fetched.seed == 42
            assert fetched.last_access > 0

            tile = fetched.hands["east"][0]
            after = await repo.discard(created.game_id, "east", tile)
            assert len(after.wall) == 82
            assert after.current_player == "south"
            assert after.discards["east"] == [tile]
            assert len(after.hands["east"]) == 13
            assert len(after.hands["south"]) == 14  # 下家自动摸一张
            assert after.version == 1
            assert _total_tiles(after) == 136

            # 清空后按 id 查不到 → 与内存 / SQL 相同的 404 语义，索引也一起清掉
            await repo.clear()
            with pytest.raises(GameNotFoundError):
                await repo.get(created.game_id)
            assert await client.zcard(INDEX_KEY) == 0
            assert await client.exists(game_key(created.game_id)) == 0
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_不存在的对局查询与打牌都抛GameNotFound() -> None:
    """不存在的 game_id：get / discard 都抛 ``GameNotFoundError``（映射 404）。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo()
        try:
            with pytest.raises(GameNotFoundError) as not_found:
                await repo.get("no-such-game")
            assert not_found.value.code == "GAME_NOT_FOUND"
            assert not_found.value.status_code == 404

            with pytest.raises(GameNotFoundError):
                await repo.discard("no-such-game", "east", "1m")

            # clear 后同样查不到（语义与内存实现一致）
            state = await repo.create(seed=1)
            await repo.clear()
            with pytest.raises(GameNotFoundError):
                await repo.get(state.game_id)
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_打牌后牌墙递减轮转下家且可连续打牌() -> None:
    """连续两次打牌：牌墙 83 → 82 → 81，当前玩家 east → south → west，总牌数守恒。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo()
        try:
            state = await repo.create(seed=11)
            state = await repo.discard(state.game_id, "east", state.hands["east"][0])
            assert len(state.wall) == 82
            assert state.current_player == "south"

            south_tile = state.hands["south"][0]
            state = await repo.discard(state.game_id, "south", south_tile)
            assert len(state.wall) == 81
            assert state.current_player == "west"
            assert state.discards["south"] == [south_tile]
            assert state.version == 2
            assert _total_tiles(state) == 136
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 3. 非法输入：错误类型与错误码和内存 / SQL 实现一致，且不落状态
# --------------------------------------------------------------------------- #


def test_非法牌与不在手与非当前回合的错误码与其他实现一致() -> None:
    """``HAND_INVALID`` / ``TILE_NOT_IN_HAND`` / ``NOT_YOUR_TURN`` 三种错误码一一对齐。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo()
        try:
            state = await repo.create(seed=9)

            with pytest.raises(InvalidHandError) as invalid:
                await repo.discard(state.game_id, "east", "9q")
            assert invalid.value.code == "HAND_INVALID"
            assert invalid.value.status_code == 400

            missing = next(tile for tile in TILE_KINDS if tile not in state.hands["east"])
            with pytest.raises(TileNotInHandError) as not_in_hand:
                await repo.discard(state.game_id, "east", missing)
            assert not_in_hand.value.code == "TILE_NOT_IN_HAND"

            with pytest.raises(AppError) as not_your_turn:
                await repo.discard(state.game_id, "south", state.hands["south"][0])
            assert not_your_turn.value.code == "NOT_YOUR_TURN"
            assert not_your_turn.value.status_code == 400

            # 失败的打牌不能留下任何状态变更（版本号 / 牌河 / 牌墙都不动）
            final = await repo.get(state.game_id)
            assert final.version == 0
            assert all(pile == [] for pile in final.discards.values())
            assert len(final.wall) == 83
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 4. 三种实现一致性：同 seed 同牌局；同打牌序列同快照
# --------------------------------------------------------------------------- #


def test_相同种子在三种实现下发出同一副牌() -> None:
    """内存 / SQL(SQLite) / Redis 共用 ``new_game_state``：同 seed 的牌局完全一致。"""

    async def scenario() -> None:
        memory = GameStore()
        sql_repo, engine = await _open_sql_repo()
        redis_repo, client = await _open_redis_repo()
        try:
            memory_state = await memory.create(seed=42)
            sql_state = await sql_repo.create(seed=42)
            redis_state = await redis_repo.create(seed=42)
        finally:
            await engine.dispose()
            await client.aclose()

        assert redis_state.hands == sql_state.hands == memory_state.hands
        assert redis_state.wall == sql_state.wall == memory_state.wall
        assert redis_state.current_player == sql_state.current_player == "east"
        assert redis_state.created_at.tzinfo is not None  # 带时区的 ISO 8601 读回

    asyncio.run(scenario())


def test_相同打牌序列在三种实现下得到相同快照() -> None:
    """同 seed 同打牌序列 → wall/hands/discards/current_player 逐字段一致。"""

    async def scenario() -> None:
        memory = GameStore()
        sql_repo, engine = await _open_sql_repo()
        redis_repo, client = await _open_redis_repo()
        try:
            stores: list[GameRepository] = [memory, sql_repo, redis_repo]
            states = [await store.create(seed=7) for store in stores]
            assert len(set(states[0].hands["east"])) > 1  # 保证有牌可打

            first_tile = states[0].hands["east"][0]
            for store, state in zip(stores, states, strict=True):
                await store.discard(state.game_id, "east", first_tile)
            after = [await store.get(state.game_id) for store, state in zip(stores, states, strict=True)]
            assert _fingerprint(after[1]) == _fingerprint(after[0])
            assert _fingerprint(after[2]) == _fingerprint(after[0])

            second_tile = after[0].hands["south"][0]
            for store, state in zip(stores, states, strict=True):
                await store.discard(state.game_id, "south", second_tile)
            final = [await store.get(state.game_id) for store, state in zip(stores, states, strict=True)]
            assert _fingerprint(final[1]) == _fingerprint(final[0])
            assert _fingerprint(final[2]) == _fingerprint(final[0])
            assert len(final[2].wall) == 81
            assert SEATS == ("east", "south", "west", "north")
        finally:
            await engine.dispose()
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 5. TTL 与上限淘汰
# --------------------------------------------------------------------------- #


def test_TTL到期后按id查不到() -> None:
    """``ttl_seconds=1`` 建局，等 1.2 秒后 get → ``GameNotFoundError``，索引也被清理。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo(ttl_seconds=1)
        try:
            state = await repo.create(seed=5)
            assert await client.exists(game_key(state.game_id)) == 1
            await asyncio.sleep(1.2)
            with pytest.raises(GameNotFoundError):
                await repo.get(state.game_id)
            # 惰性清理：过期对局的索引项也会被 ZRANGEBYSCORE 扫掉
            assert await client.zcard(INDEX_KEY) == 0
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_对局数上限淘汰最久未访问的对局() -> None:
    """``max_games=2`` 建 3 局：ZSET 里 score 最小（最久未访问）的一局被 ZPOPMIN 淘汰。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo(max_games=2, ttl_seconds=7200)
        try:
            first = await repo.create(seed=1)
            second = await repo.create(seed=2)
            # 显式把 first 的索引 score 回拨 1000 秒（仍在 TTL 内），不依赖 time.time() 精度
            await client.zadd(INDEX_KEY, {first.game_id: time.time() - 1000})

            third = await repo.create(seed=3)
            with pytest.raises(GameNotFoundError):
                await repo.get(first.game_id)
            assert (await repo.get(second.game_id)).game_id == second.game_id
            assert (await repo.get(third.game_id)).game_id == third.game_id
            assert await client.zcard(INDEX_KEY) == 2
        finally:
            await client.aclose()

    asyncio.run(scenario())


def test_访问会刷新LRU位置() -> None:
    """访问 A 后 A 的 score 变新，再建第 3 局时淘汰的是 B 而不是 A。"""

    async def scenario() -> None:
        repo, client = await _open_redis_repo(max_games=2, ttl_seconds=7200)
        try:
            first = await repo.create(seed=1)
            second = await repo.create(seed=2)
            # 把 first 的索引 score 回拨 1000 秒（仍在 TTL 内），让它在 LRU 里「看起来最旧」
            await client.zadd(INDEX_KEY, {first.game_id: time.time() - 1000})
            score_before = await client.zscore(INDEX_KEY, first.game_id)

            await asyncio.sleep(0.02)  # 保证刷新后的时间戳严格更大，不依赖时钟精度
            await repo.get(first.game_id)  # 访问 → score 刷新到最新
            score_after = await client.zscore(INDEX_KEY, first.game_id)
            second_score = await client.zscore(INDEX_KEY, second.game_id)
            assert score_before is not None and score_after is not None and second_score is not None
            assert score_after > score_before
            assert score_after > second_score

            third = await repo.create(seed=3)
            with pytest.raises(GameNotFoundError):
                await repo.get(second.game_id)
            assert (await repo.get(first.game_id)).game_id == first.game_id
            assert (await repo.get(third.game_id)).game_id == third.game_id
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 6. 并发打牌：WATCH + MULTI/EXEC 乐观锁，只有一次成功
# --------------------------------------------------------------------------- #


def test_15个并发打牌只有一次成功且状态不错乱() -> None:
    """15 个并发打同一张牌：1 次成功、14 次 409 冲突；牌河只多 1 张、总牌数守恒 136。"""

    async def scenario() -> None:
        client = _new_fake_client()
        repo = _SlowRedisGameRepository(client)  # 放大 WATCH 读与 EXEC 之间的并发窗口
        try:
            state = await repo.create(seed=21)
            tile = state.hands["east"][0]

            results = await asyncio.gather(
                *(repo.discard(state.game_id, "east", tile) for _ in range(15)),
                return_exceptions=True,
            )
            successes = [result for result in results if isinstance(result, GameState)]
            failures = [result for result in results if isinstance(result, BaseException)]
            assert len(successes) == 1, results
            assert len(failures) == 14, results
            # WATCH 检测到别人提交过 → WatchError → 409 GAME_CONFLICT，绝不静默覆盖
            for failure in failures:
                assert isinstance(failure, ConcurrentUpdateError)
                assert failure.code == "GAME_CONFLICT"
                assert failure.status_code == 409

            final = await repo.get(state.game_id)
            assert final.discards["east"] == [tile]  # 只有一次打牌生效
            assert final.current_player == "south"
            assert len(final.wall) == 82
            assert final.version == 1
            assert _total_tiles(final) == 136
        finally:
            await client.aclose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 7. 真实 API 闭环：create_app(settings) + TestClient（注入 fakeredis）
# --------------------------------------------------------------------------- #


def test_Redis模式下通过API完成建局查询打牌闭环(monkeypatch: pytest.MonkeyPatch) -> None:
    """配置 ``REDIS_URL`` 后 lifespan 选 Redis 实现，HTTP 层零改动走完建局 / 查询 / 打牌。"""
    created_clients: list[Any] = []

    def _factory(redis_url: str) -> Any:
        """替身工厂：在被测 lifespan 的事件循环里创建 fakeredis，避免跨 loop 使用。"""
        assert redis_url.startswith("redis://")
        fake = fakeredis.aioredis.FakeRedis(decode_responses=True)
        created_clients.append(fake)
        return fake

    monkeypatch.setattr(redis_module, "create_redis_client", _factory)
    app = create_app(Settings(redis_url="redis://127.0.0.1:6379/0", game_ttl_seconds=3600))

    with TestClient(app) as client:
        assert isinstance(app.state.store, RedisGameRepository)
        assert app.state.db_engine is None
        assert app.state.redis_client is created_clients[0]

        created_response = client.post("/api/v1/games", json={"seed": 42})
        assert created_response.status_code == 200, created_response.text
        created = created_response.json()
        game_id = created["game_id"]
        assert created["wall_remaining"] == 83
        assert created["current_player"] == "east"
        assert len(created["hands"]["east"]) == 14

        detail = client.get(f"/api/v1/games/{game_id}")
        assert detail.status_code == 200
        assert len(detail.json()["hands"]["east"]) == 14
        assert detail.json()["hands"]["south"] == 13  # 其余座位只暴露张数

        tile = created["hands"]["east"][0]
        discard_response = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": tile}
        )
        assert discard_response.status_code == 200, discard_response.text
        body = discard_response.json()
        assert body["wall_remaining"] == 82
        assert body["next_player"] == "south"
        assert body["discards"]["east"] == [tile]

        # 错误路径同样走 Redis 实现，统一错误信封不变
        bad_tile = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "south", "tile": "9q"}
        )
        assert bad_tile.status_code == 400
        assert bad_tile.json()["error"]["code"] == "HAND_INVALID"

        missing_game = client.get("/api/v1/games/no-such-game")
        assert missing_game.status_code == 404
        assert missing_game.json()["error"]["code"] == "GAME_NOT_FOUND"

        refreshed = client.get(f"/api/v1/games/{game_id}").json()
        assert refreshed["current_player"] == "south"
        assert refreshed["wall_remaining"] == 82
        assert refreshed["hands"]["south"] == 14

    assert len(created_clients) == 1
