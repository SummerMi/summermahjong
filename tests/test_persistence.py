"""持久化分支测试：抽象接口、SQL 实现、打牌原子性、真实 API 闭环。

覆盖点（对应 ``docs/adr-0002-持久化选型.md``）：

1. **默认零依赖**：``DATABASE_URL`` 为空时拿到的是内存 ``GameStore``，行为与改造前一致；
2. **可切换**：配置 SQLite DSN 后拿到 ``SqlGameRepository``，路由代码零改动；
3. **真落库**：换一个 repository / engine 实例仍能读到同一局（不是进程内假持久化）；
4. **行为一致**：相同 seed 在两种实现下发出同一副牌；非法牌抛同样的错误码；
5. **原子性**：并发打同一局只有一次成功，状态不错乱、总牌数守恒（136 张）；
6. **全链路**：``create_app(settings)`` 注入 SQL 模式后，TestClient 走真实 HTTP 请求；
7. **重启可查**：应用重启（engine dispose 后重建）仍能按 game_id 查到对局。

约定：与本项目其他测试一致，异步场景用 ``asyncio.run`` 包裹，不额外引入 pytest-asyncio。
"""

import asyncio
import time
from pathlib import Path
from typing import Any

import pytest
from fastapi.testclient import TestClient
from sqlalchemy import func, select, update
from sqlalchemy.exc import SQLAlchemyError

from app.core.config import Settings
from app.core.errors import AppError, GameNotFoundError, InvalidHandError, TileNotInHandError
from app.main import create_app
from app.services.mahjong.tiles import TILE_KINDS
from app.services.repository.base import (
    SEATS,
    ConcurrentUpdateError,
    GameRepository,
    GameState,
)
from app.services.repository.sql import (
    GameRow,
    SqlGameRepository,
    create_engine_from_url,
    init_models,
    mask_dsn,
)
from app.services.store import GameStore


def _sqlite_url(tmp_path: Path, name: str = "games.db") -> str:
    """拼一个 SQLite 文件库 DSN（用 posix 路径，避免 Windows 反斜杠被当成转义）。"""
    return f"sqlite+aiosqlite:///{(tmp_path / name).as_posix()}"


async def _open_repo(url: str, **kwargs: Any) -> tuple[SqlGameRepository, Any]:
    """建表 + 建仓库，返回 ``(repository, engine)``，调用方负责 ``await engine.dispose()``。"""
    engine = create_engine_from_url(url)
    await init_models(engine)
    return SqlGameRepository(engine, **kwargs), engine


def _total_tiles(state: GameState) -> int:
    """总牌数守恒校验：牌墙 + 四家手牌 + 四家牌河 == 136。"""
    return (
        len(state.wall)
        + sum(len(hand) for hand in state.hands.values())
        + sum(len(pile) for pile in state.discards.values())
    )


# --------------------------------------------------------------------------- #
# 1. 配置决定实现：默认内存、配置后 SQL
# --------------------------------------------------------------------------- #


def test_默认配置使用内存实现() -> None:
    """DATABASE_URL 为空 → 内存 GameStore，且没有创建任何数据库 engine。"""
    app = create_app(Settings(database_url="", db_echo=False))
    with TestClient(app) as client:
        assert isinstance(app.state.store, GameStore)
        assert isinstance(app.state.store, GameRepository)
        assert app.state.db_engine is None
        # 零依赖链路照常可用
        response = client.post("/api/v1/games", json={"seed": 42})
        assert response.status_code == 200
        assert response.json()["wall_remaining"] == 83


def test_配置数据库地址后切换到SQL实现(tmp_path: Path) -> None:
    """DATABASE_URL 非空 → SqlGameRepository + 异步 engine，且已建表。"""
    url = _sqlite_url(tmp_path)
    app = create_app(Settings(database_url=url))
    with TestClient(app):
        assert isinstance(app.state.store, SqlGameRepository)
        assert isinstance(app.state.store, GameRepository)
        assert app.state.db_engine is not None

    # lifespan 已经 create_all：换一个全新 engine 直接 count 不报错，说明表真的建好了
    async def count_rows() -> int:
        engine = create_engine_from_url(url)
        try:
            async with engine.connect() as conn:
                return (await conn.execute(select(func.count()).select_from(GameRow))).scalar_one()
        finally:
            await engine.dispose()

    assert asyncio.run(count_rows()) == 0


def test_两个实现都满足仓库接口() -> None:
    """路由只依赖抽象：内存实现与 SQL 实现都是 GameRepository 的子类。"""
    assert isinstance(GameStore(), GameRepository)
    assert issubclass(SqlGameRepository, GameRepository)


# --------------------------------------------------------------------------- #
# 2. SQL 实现的基本读写
# --------------------------------------------------------------------------- #


def test_SQL仓库完成创建查询打牌清空全流程(tmp_path: Path) -> None:
    """create → get → discard → clear 全流程，语义与内存实现一致。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            created = await repo.create(seed=42)
            assert created.wall and len(created.wall) == 83
            assert created.current_player == "east"

            fetched = await repo.get(created.game_id)
            assert fetched.hands == created.hands
            assert fetched.seed == 42

            tile = fetched.hands["east"][0]
            after = await repo.discard(created.game_id, "east", tile)
            assert len(after.wall) == 82
            assert after.current_player == "south"
            assert after.discards["east"] == [tile]

            # 清空后按 id 查不到 → 与内存实现相同的 404 语义
            await repo.clear()
            with pytest.raises(GameNotFoundError):
                await repo.get(created.game_id)
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_对局状态可跨仓库实例读取(tmp_path: Path) -> None:
    """真落库验证：dispose 旧 engine、新开 engine + 新 repository 仍能读到完整状态。"""
    url = _sqlite_url(tmp_path)

    async def write_phase() -> tuple[str, GameState]:
        repo, engine = await _open_repo(url)
        try:
            state = await repo.create(seed=7)
            tile = state.hands["east"][0]
            return state.game_id, await repo.discard(state.game_id, "east", tile)
        finally:
            await engine.dispose()

    async def read_phase(game_id: str) -> GameState:
        repo, engine = await _open_repo(url)
        try:
            return await repo.get(game_id)
        finally:
            await engine.dispose()

    game_id, after_discard = asyncio.run(write_phase())
    reloaded = asyncio.run(read_phase(game_id))

    assert reloaded.game_id == game_id
    assert reloaded.seed == 7
    assert reloaded.current_player == "south"
    assert reloaded.discards["east"] == after_discard.discards["east"]
    assert reloaded.hands == after_discard.hands
    assert len(reloaded.wall) == 82
    assert reloaded.version == 1  # 版本号也一起持久化了


def test_相同种子在内存与SQL实现下发出同一副牌(tmp_path: Path) -> None:
    """两种实现共用 ``new_game_state``：同 seed 的牌局完全一致（换存储不换行为）。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            sql_state = await repo.create(seed=42)
            memory_state = await GameStore().create(seed=42)
        finally:
            await engine.dispose()

        assert sql_state.hands == memory_state.hands
        assert sql_state.wall == memory_state.wall
        assert sql_state.current_player == memory_state.current_player == "east"

    asyncio.run(scenario())


def test_SQL打牌后牌墙递减牌进牌河并轮转下家(tmp_path: Path) -> None:
    """打牌三步：手牌 -1 进牌河、牌墙 -1（下家摸走）、轮转到南家；总牌数守恒。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            state = await repo.create(seed=11)
            east_hand_before = list(state.hands["east"])
            tile = east_hand_before[0]

            after = await repo.discard(state.game_id, "east", tile)
            assert len(after.wall) == 82
            assert after.discards["east"] == [tile]
            assert after.current_player == "south"
            assert len(after.hands["east"]) == 13
            assert len(after.hands["south"]) == 14  # 下家自动摸了一张
            # 手里的同种牌可能有重复，所以按张数比较而不是按包含关系
            assert after.hands["east"].count(tile) == east_hand_before.count(tile) - 1
            assert _total_tiles(after) == 136

            # 再打一次，轮到南家
            south_tile = after.hands["south"][0]
            third = await repo.discard(state.game_id, "south", south_tile)
            assert third.current_player == "west"
            assert len(third.wall) == 81
            assert third.discards["south"] == [south_tile]
            assert _total_tiles(third) == 136
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_版本号随每次写入递增(tmp_path: Path) -> None:
    """version 是乐观锁的载体：create=0，每打一次牌 +1，读操作不改变它。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            state = await repo.create(seed=3)
            assert state.version == 0

            state = await repo.discard(state.game_id, "east", state.hands["east"][0])
            assert state.version == 1

            fetched = await repo.get(state.game_id)
            assert fetched.version == 1
            assert fetched.current_player == "south"
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_内存数据库URL通过StaticPool也可用() -> None:
    """``sqlite+aiosqlite:///:memory:`` 单连接池：建表与读写落在同一条连接上。"""

    async def scenario() -> None:
        repo, engine = await _open_repo("sqlite+aiosqlite:///:memory:")
        try:
            state = await repo.create(seed=5)
            fetched = await repo.get(state.game_id)
            assert fetched.hands == state.hands
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_SQL实现遵守对局数上限与TTL清理(tmp_path: Path) -> None:
    """LRU 淘汰与 TTL 清理语义和内存实现一致（上限 2：建第 3 局淘汰最久未访问的）。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path), max_games=2, ttl_seconds=60)
        try:
            first = await repo.create(seed=1)
            second = await repo.create(seed=2)
            # 显式把 first 的 last_access 回拨（不依赖 time.time() 的精度来分先后）
            async with engine.begin() as conn:
                await conn.execute(
                    update(GameRow)
                    .where(GameRow.game_id == first.game_id)
                    .values(last_access=time.time() - 1000)
                )
            third = await repo.create(seed=3)
            with pytest.raises(GameNotFoundError):
                await repo.get(first.game_id)
            assert (await repo.get(second.game_id)).game_id == second.game_id
            assert (await repo.get(third.game_id)).game_id == third.game_id

            # 把 last_access 回拨到超过 TTL，下次访问即被清理
            async with engine.begin() as conn:
                await conn.execute(
                    update(GameRow)
                    .where(GameRow.game_id == third.game_id)
                    .values(last_access=time.time() - 61)
                )
            with pytest.raises(GameNotFoundError):
                await repo.get(third.game_id)
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 3. 非法输入：错误类型与错误码必须与内存实现一致
# --------------------------------------------------------------------------- #


def test_非法牌在两种实现下抛出相同错误(tmp_path: Path) -> None:
    """未知牌面 → InvalidHandError / HAND_INVALID；SQL 与内存实现行为一致。"""

    async def scenario() -> None:
        memory = GameStore()
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            memory_state = await memory.create(seed=1)
            sql_state = await repo.create(seed=1)

            with pytest.raises(InvalidHandError) as memory_error:
                await memory.discard(memory_state.game_id, "east", "9q")
            with pytest.raises(InvalidHandError) as sql_error:
                await repo.discard(sql_state.game_id, "east", "9q")
            assert memory_error.value.code == sql_error.value.code == "HAND_INVALID"
            assert memory_error.value.status_code == sql_error.value.status_code == 400

            # 失败的打牌不能留下任何状态变更
            assert (await repo.get(sql_state.game_id)).discards["east"] == []
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_牌不在手与非当前玩家在SQL实现下返回同样的业务错误(tmp_path: Path) -> None:
    """TILE_NOT_IN_HAND（牌不在手）与 NOT_YOUR_TURN（抢先打牌）的错误码保持一致。"""

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            state = await repo.create(seed=9)
            missing = next(tile for tile in TILE_KINDS if tile not in state.hands["east"])
            with pytest.raises(TileNotInHandError) as not_in_hand:
                await repo.discard(state.game_id, "east", missing)
            assert not_in_hand.value.code == "TILE_NOT_IN_HAND"

            with pytest.raises(AppError) as not_your_turn:
                await repo.discard(state.game_id, "south", state.hands["south"][0])
            assert not_your_turn.value.code == "NOT_YOUR_TURN"
            assert not_your_turn.value.status_code == 400

            with pytest.raises(GameNotFoundError):
                await repo.discard("不存在的对局", "east", "1m")
        finally:
            await engine.dispose()

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 4. 并发打牌：只能有一次成功
# --------------------------------------------------------------------------- #


def test_并发打同一局只有一次成功且状态不错乱(tmp_path: Path) -> None:
    """两个并发的同一张牌请求 → 一次成功；另一次是 409 版本冲突 / 非当前回合 / 锁等待失败。

    SQLite 没有真正的行锁，靠「事务 + version 条件更新」兜底；MySQL 下同一段代码
    会先 ``SELECT ... FOR UPDATE`` 串行化。无论哪种最终都必须满足：牌河只多 1 张、
    总牌数守恒、当前玩家正确轮转。
    """

    async def scenario() -> None:
        repo, engine = await _open_repo(_sqlite_url(tmp_path))
        try:
            state = await repo.create(seed=21)
            tile = state.hands["east"][0]

            results = await asyncio.gather(
                repo.discard(state.game_id, "east", tile),
                repo.discard(state.game_id, "east", tile),
                return_exceptions=True,
            )
            successes = [result for result in results if isinstance(result, GameState)]
            failures = [result for result in results if isinstance(result, BaseException)]
            assert len(successes) == 1, results
            assert len(failures) == 1, results
            # 允许的失败形态：乐观锁冲突、被串行化后检测到不是当前回合、SQLite 锁等待失败
            assert isinstance(failures[0], (ConcurrentUpdateError, AppError, SQLAlchemyError))

            final = await repo.get(state.game_id)
            assert final.discards["east"] == [tile]  # 只有一次打牌生效
            assert final.current_player == "south"
            assert len(final.wall) == 82
            assert final.version == 1
            assert _total_tiles(final) == 136
        finally:
            await engine.dispose()

    asyncio.run(scenario())


def test_内存实现并发打牌同样只有一次成功() -> None:
    """内存实现用 asyncio.Lock 串行化：一次成功，另一次检测到不是当前回合。"""

    async def scenario() -> None:
        store = GameStore()
        state = await store.create(seed=21)
        tile = state.hands["east"][0]

        results = await asyncio.gather(
            store.discard(state.game_id, "east", tile),
            store.discard(state.game_id, "east", tile),
            return_exceptions=True,
        )
        successes = [result for result in results if isinstance(result, GameState)]
        assert len(successes) == 1
        errors = [result for result in results if isinstance(result, BaseException)]
        assert len(errors) == 1
        assert isinstance(errors[0], AppError)
        assert errors[0].code == "NOT_YOUR_TURN"

        final = await store.get(state.game_id)
        assert final.discards["east"] == [tile]
        assert final.current_player == "south"

    asyncio.run(scenario())


# --------------------------------------------------------------------------- #
# 5. 真实 API 闭环（TestClient 走完整 HTTP 栈）
# --------------------------------------------------------------------------- #


def test_通过API在SQL模式下完成建局查询打牌闭环(tmp_path: Path) -> None:
    """``create_app(Settings(database_url=...))`` → 建局 → 查询 → 打牌 → 错误路径。"""
    app = create_app(Settings(database_url=_sqlite_url(tmp_path), game_ttl_seconds=3600))
    with TestClient(app) as client:
        created_response = client.post("/api/v1/games", json={"seed": 42})
        assert created_response.status_code == 200, created_response.text
        created = created_response.json()
        game_id = created["game_id"]
        assert created["wall_remaining"] == 83
        assert created["current_player"] == "east"

        detail = client.get(f"/api/v1/games/{game_id}")
        assert detail.status_code == 200
        assert len(detail.json()["hands"]["east"]) == 14
        assert detail.json()["hands"]["south"] == 13

        tile = created["hands"]["east"][0]
        discard_response = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": tile}
        )
        assert discard_response.status_code == 200, discard_response.text
        body = discard_response.json()
        assert body["wall_remaining"] == 82
        assert body["next_player"] == "south"
        assert body["discards"]["east"] == [tile]

        # 错误路径也走 SQL 实现，统一信封不变
        bad_tile = client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "south", "tile": "9q"}
        )
        assert bad_tile.status_code == 400
        assert bad_tile.json()["error"]["code"] == "HAND_INVALID"

        missing_game = client.get("/api/v1/games/no-such-game")
        assert missing_game.status_code == 404
        assert missing_game.json()["error"]["code"] == "GAME_NOT_FOUND"

        # 查询也命中库里已更新的状态
        refreshed = client.get(f"/api/v1/games/{game_id}").json()
        assert refreshed["current_player"] == "south"
        assert refreshed["wall_remaining"] == 82
        assert refreshed["hands"]["south"] == 14


def test_应用重启后同一局仍可查询(tmp_path: Path) -> None:
    """持久化最核心的价值：进程重启（engine dispose + 重建）后按 game_id 仍查得到。"""
    url = _sqlite_url(tmp_path)

    with TestClient(create_app(Settings(database_url=url))) as first_client:
        created = first_client.post("/api/v1/games", json={"seed": 42}).json()
        game_id = created["game_id"]
        tile = created["hands"]["east"][0]
        first_client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": tile}
        )

    # 新应用实例（等价于重启进程）用同一个库
    with TestClient(create_app(Settings(database_url=url))) as second_client:
        response = second_client.get(f"/api/v1/games/{game_id}")
        assert response.status_code == 200, response.text
        body = response.json()
        assert body["game_id"] == game_id
        assert body["current_player"] == "south"
        assert body["wall_remaining"] == 82
        assert body["hands"]["south"] == 14

        # 重启后还能继续打牌（错误路径同样走 SQL 实现）
        follow_up = second_client.post(
            f"/api/v1/games/{game_id}/discard", json={"seat": "south", "tile": "9q"}
        )
        assert follow_up.status_code == 400
        assert follow_up.json()["error"]["code"] == "HAND_INVALID"


def test_DSN中的密码不会泄漏到日志(tmp_path: Path) -> None:
    """启动日志会打印存储配置，DSN 里的密码必须先掩码。"""
    assert (
        mask_dsn("mysql+aiomysql://root:secret@127.0.0.1:3306/mahjong")
        == "mysql+aiomysql://root:***@127.0.0.1:3306/mahjong"
    )
    assert mask_dsn("sqlite+aiosqlite:///./games.db") == "sqlite+aiosqlite:///./games.db"
    assert mask_dsn("postgresql+asyncpg://user@db:5432/x") == "postgresql+asyncpg://user@db:5432/x"


def test_SQL仓库的SEATS与内存实现一致() -> None:
    """两种实现必须用同一份座位定义，否则轮转顺序会分叉。"""
    assert SEATS == ("east", "south", "west", "north")
