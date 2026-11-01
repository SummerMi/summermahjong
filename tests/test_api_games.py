"""对局端点测试：创建 / 查询 / 打牌 + 仓库上限与 TTL。

关键覆盖点：

- 相同 seed 可复现同一牌局（面试常问“怎么保证测试稳定 / 复盘”）；
- 查询对局只暴露庄家手牌（信息隐藏）；
- 打牌的成功路径与 400（未知牌 / 牌不在手 / 非当前玩家）、404、422；
- GameStore 的数量上限淘汰与 TTL 清理（直接单测仓库）。
"""

import asyncio
from collections.abc import Iterator
from typing import Any

import pytest
from fastapi.testclient import TestClient

from app.core.config import Settings
from app.core.errors import GameNotFoundError
from app.main import create_app
from app.services.mahjong.tiles import TILE_KINDS
from app.services.store import GameStore


@pytest.fixture()
def client() -> Iterator[TestClient]:
    """每个用例一个独立应用实例（独立内存对局仓库）。"""
    with TestClient(create_app()) as test_client:
        yield test_client


def _create_game(client: TestClient, seed: int | None = 42) -> dict[str, Any]:
    """创建一局并断言成功，返回响应体。"""
    payload = {} if seed is None else {"seed": seed}
    response = client.post("/api/v1/games", json=payload)
    assert response.status_code == 200, response.text
    return response.json()


def test_创建对局返回四家手牌(client: TestClient) -> None:
    """发牌后：庄家 14 张、其余各 13 张，牌墙剩 83 张。"""
    data = _create_game(client)
    assert len(data["game_id"]) == 32
    assert data["wall_remaining"] == 83
    assert data["current_player"] == "east"
    assert {seat: len(hand) for seat, hand in data["hands"].items()} == {
        "east": 14,
        "south": 13,
        "west": 13,
        "north": 13,
    }
    assert data["created_at"]


def test_相同种子可复现牌局(client: TestClient) -> None:
    """同一 seed 两次创建 → 四家手牌完全一致。"""
    first = _create_game(client, seed=42)
    second = _create_game(client, seed=42)
    assert first["game_id"] != second["game_id"]
    assert first["hands"] == second["hands"]
    assert first["wall_remaining"] == second["wall_remaining"]


def test_不同种子牌局不同(client: TestClient) -> None:
    """不同 seed → 手牌不同（洗牌随机源独立，不污染全局 random）。"""
    first = _create_game(client, seed=42)
    second = _create_game(client, seed=7)
    assert first["hands"] != second["hands"]


def test_省略种子也能创建(client: TestClient) -> None:
    """seed 可省略：无请求体或空对象都能创建（随机牌局）。"""
    without_body = client.post("/api/v1/games")
    assert without_body.status_code == 200
    assert without_body.json()["wall_remaining"] == 83

    empty_body = client.post("/api/v1/games", json={})
    assert empty_body.status_code == 200
    assert empty_body.json()["hands"]["east"] != without_body.json()["hands"]["east"]


def test_创建对局种子类型错误返回422(client: TestClient) -> None:
    """seed 传字符串属于格式问题 → 422。"""
    response = client.post("/api/v1/games", json={"seed": "abc"})
    assert response.status_code == 422


def test_查询对局只暴露庄家手牌(client: TestClient) -> None:
    """hands 里 east 是完整手牌，其余座位只给张数。"""
    game_id = _create_game(client)["game_id"]
    response = client.get(f"/api/v1/games/{game_id}")
    assert response.status_code == 200
    data = response.json()
    assert isinstance(data["hands"]["east"], list)
    assert len(data["hands"]["east"]) == 14
    for seat in ("south", "west", "north"):
        assert data["hands"][seat] == 13


def test_查询不存在对局返回404(client: TestClient) -> None:
    """不存在的 game_id → 404 GAME_NOT_FOUND。"""
    response = client.get("/api/v1/games/不存在的对局")
    assert response.status_code == 404
    assert response.json()["error"]["code"] == "GAME_NOT_FOUND"


def test_打牌成功牌进牌河并轮转下家(client: TestClient) -> None:
    """打出一张：牌进牌河、轮转南家、南家自动摸牌（牌墙 83 → 82）。"""
    created = _create_game(client)
    game_id = created["game_id"]
    tile = created["hands"]["east"][0]

    response = client.post(
        f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": tile}
    )
    assert response.status_code == 200
    body = response.json()
    assert body["seat"] == "east"
    assert body["tile"] == tile
    assert body["wall_remaining"] == 82
    assert body["next_player"] == "south"
    assert body["discards"]["east"] == [tile]

    detail = client.get(f"/api/v1/games/{game_id}").json()
    assert detail["current_player"] == "south"
    assert detail["hands"]["south"] == 14
    assert detail["wall_remaining"] == 82


def test_打牌牌不在手返回400(client: TestClient) -> None:
    """打一张自己手里没有的牌 → 400 TILE_NOT_IN_HAND。"""
    created = _create_game(client)
    game_id = created["game_id"]
    east_hand = set(created["hands"]["east"])
    missing = next(tile for tile in TILE_KINDS if tile not in east_hand)

    response = client.post(
        f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": missing}
    )
    assert response.status_code == 400
    body = response.json()
    assert body["error"]["code"] == "TILE_NOT_IN_HAND"
    assert missing in body["error"]["message"]


def test_打牌未知牌返回400(client: TestClient) -> None:
    """牌面本身非法 → 400 HAND_INVALID。"""
    created = _create_game(client)
    response = client.post(
        f"/api/v1/games/{created['game_id']}/discard",
        json={"seat": "east", "tile": "9q"},
    )
    assert response.status_code == 400
    assert response.json()["error"]["code"] == "HAND_INVALID"


def test_打牌非当前玩家返回400(client: TestClient) -> None:
    """开局是东家出牌，南家抢先打 → 400 NOT_YOUR_TURN。"""
    created = _create_game(client)
    response = client.post(
        f"/api/v1/games/{created['game_id']}/discard",
        json={"seat": "south", "tile": "1m"},
    )
    assert response.status_code == 400
    body = response.json()
    assert body["error"]["code"] == "NOT_YOUR_TURN"
    assert "east" in body["error"]["message"]


def test_打牌对局不存在返回404(client: TestClient) -> None:
    """对局不存在时先返回 404，而不是 400。"""
    response = client.post(
        "/api/v1/games/no-such-game/discard", json={"seat": "east", "tile": "1m"}
    )
    assert response.status_code == 404
    assert response.json()["error"]["code"] == "GAME_NOT_FOUND"


def test_打牌座位非法返回422(client: TestClient) -> None:
    """seat 不在枚举内属于格式问题 → 422。"""
    game_id = _create_game(client)["game_id"]
    response = client.post(
        f"/api/v1/games/{game_id}/discard", json={"seat": "east2", "tile": "1m"}
    )
    assert response.status_code == 422


def test_打牌缺少字段返回422(client: TestClient) -> None:
    """缺少 tile 字段 → 422。"""
    game_id = _create_game(client)["game_id"]
    response = client.post(f"/api/v1/games/{game_id}/discard", json={"seat": "east"})
    assert response.status_code == 422


def test_对局数超上限时淘汰最久未访问的对局() -> None:
    """max_games=2 时创建第 3 局会淘汰最久未访问的一局（演示用 LRU 策略）。"""
    settings = Settings(max_games=2, game_ttl_seconds=3600)
    with TestClient(create_app(settings)) as client:
        game_ids = [
            client.post("/api/v1/games", json={"seed": seed}).json()["game_id"]
            for seed in (1, 2, 3)
        ]
        assert client.get(f"/api/v1/games/{game_ids[0]}").status_code == 404
        assert client.get(f"/api/v1/games/{game_ids[1]}").status_code == 200
        assert client.get(f"/api/v1/games/{game_ids[2]}").status_code == 200


def test_闲置超时的对局会被清理() -> None:
    """把 last_access 回拨到超过 TTL，下一次访问即触发清理并返回 404。"""

    async def scenario() -> None:
        store = GameStore(max_games=10, ttl_seconds=60)
        state = await store.create(seed=1)
        state.last_access -= 61  # 模拟闲置超过 TTL
        with pytest.raises(GameNotFoundError):
            await store.get(state.game_id)

    asyncio.run(scenario())
