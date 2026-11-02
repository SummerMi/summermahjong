"""WebSocket 实时对局测试：连接 / 广播 / 心跳 / 握手拒绝 / 跨连接一致性。

用 ``TestClient.websocket_connect``（底层是 Starlette 的测试 portal，不需要
真的 websockets 服务器）。约定：

- 默认实例（``JWT_SECRET`` 为空）测试不鉴权的老路径；
- 需要鉴权的用例单独 ``create_app(Settings(jwt_secret=...))`` 注入；
- 空闲超时测试用 ``monkeypatch`` 把 ``IDLE_TIMEOUT_SECONDS`` 调小，不 sleep 60 秒。
"""

import time
from collections.abc import Callable, Iterator
from datetime import UTC, datetime, timedelta
from typing import Any

import jwt as pyjwt
import pytest
from fastapi.testclient import TestClient
from starlette.websockets import WebSocketDisconnect

from app.api.routes import ws as ws_module
from app.core.config import Settings
from app.main import create_app
from app.services.mahjong.tiles import TILE_KINDS

SECRET = "ws-test-secret-at-least-32-bytes-long"  # 测试专用密钥


@pytest.fixture()
def client() -> Iterator[TestClient]:
    """鉴权关闭（默认）的应用：连接只需要 seat 参数。"""
    with TestClient(create_app(Settings(jwt_secret=""))) as test_client:
        yield test_client


@pytest.fixture()
def auth_client() -> Iterator[TestClient]:
    """鉴权开启的应用：连接必须带 ?token=<JWT>。"""
    with TestClient(create_app(Settings(jwt_secret=SECRET, jwt_expire_minutes=60))) as test_client:
        yield test_client


def _create_game(client: TestClient, headers: dict[str, str] | None = None, seed: int = 42) -> dict[str, Any]:
    """用 HTTP 建一局（鉴权开启时传 headers），返回响应体。"""
    response = client.post("/api/v1/games", json={"seed": seed}, headers=headers)
    assert response.status_code == 200, response.text
    return response.json()


def _ws_url(game_id: str, seat: str, token: str | None = None) -> str:
    """拼 WebSocket 路径（含可选 token 查询参数）。"""
    url = f"/api/v1/ws/games/{game_id}?seat={seat}"
    return f"{url}&token={token}" if token else url


def _issue_token(client: TestClient, seat: str = "east") -> str:
    response = client.post("/api/v1/auth/token", json={"seat": seat})
    assert response.status_code == 200, response.text
    return response.json()["access_token"]


def _expect_rejected(client: TestClient, url: str, code: int) -> None:
    """断言握手被拒：close-before-accept 会让客户端在建立连接时抛 WebSocketDisconnect。"""
    with (
        pytest.raises(WebSocketDisconnect) as exc_info,
        client.websocket_connect(url) as websocket,
    ):
        websocket.receive_json()  # pragma: no cover - 正常不会执行到
    assert exc_info.value.code == code


def _wait_until(predicate: Callable[[], bool], timeout: float = 2.0) -> bool:
    """轮询等待条件成立（避免对服务端清理时序做脆弱的立即断言）。"""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.01)
    return predicate()


# --------------------------------------------------------------------------- #
# 一、连接与 presence
# --------------------------------------------------------------------------- #


def test_连接成功先收到connected再收到presence(client: TestClient) -> None:
    """第一条是 connected（含座位与牌墙），第二条是 presence 在线座位。"""
    game = _create_game(client)
    with client.websocket_connect(_ws_url(game["game_id"], "east")) as websocket:
        connected = websocket.receive_json()
        assert connected == {
            "type": "connected",
            "game_id": game["game_id"],
            "seat": "east",
            "wall_remaining": 83,
        }
        assert websocket.receive_json() == {"type": "presence", "seats_online": ["east"]}


def test_两个客户端都收到在线座位更新(client: TestClient) -> None:
    """新人加入时，老连接和新连接都会收到包含两人的 presence。"""
    game = _create_game(client)
    game_id = game["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as east:
        east.receive_json()  # connected
        east.receive_json()  # presence: ["east"]

        with client.websocket_connect(_ws_url(game_id, "south")) as south:
            assert south.receive_json()["seat"] == "south"
            assert south.receive_json() == {
                "type": "presence",
                "seats_online": ["east", "south"],
            }
            assert east.receive_json() == {
                "type": "presence",
                "seats_online": ["east", "south"],
            }


def test_在线座位按东南西北顺序返回(client: TestClient) -> None:
    """seats_online 按固定座位顺序，而不是连接顺序（先连西家再连东家）。"""
    game = _create_game(client)
    game_id = game["game_id"]
    with client.websocket_connect(_ws_url(game_id, "west")) as west:
        west.receive_json()
        west.receive_json()
        with client.websocket_connect(_ws_url(game_id, "east")) as east:
            east.receive_json()
            east.receive_json()
            manager = client.app.state.ws_manager
            assert manager.seats_online(game_id) == ["east", "west"]
            assert manager.connection_count(game_id) == 2


# --------------------------------------------------------------------------- #
# 二、打牌广播与状态同步
# --------------------------------------------------------------------------- #


def test_一个客户端打牌两个客户端都收到state(client: TestClient) -> None:
    """东家打牌：东家与南家都收到同一条 state（牌墙 83 → 82、牌河 +1）。"""
    game = _create_game(client)
    game_id = game["game_id"]
    tile = game["hands"]["east"][0]

    with client.websocket_connect(_ws_url(game_id, "east")) as east:
        east.receive_json()
        east.receive_json()
        with client.websocket_connect(_ws_url(game_id, "south")) as south:
            south.receive_json()
            south.receive_json()
            east.receive_json()  # 南家加入的 presence

            east.send_json({"type": "discard", "tile": tile})

            for websocket in (east, south):
                state = websocket.receive_json()
                assert state["type"] == "state"
                assert state["wall_remaining"] == 82
                assert state["current_player"] == "south"
                assert state["discards"]["east"] == [tile]
                assert state["last"] == {"seat": "east", "tile": tile}


def test_websocket打牌后HTTP查询看到同一份状态(client: TestClient) -> None:
    """WS 与 HTTP 共用同一个 store：WS 打完牌，HTTP GET 立刻能看到新状态。"""
    game = _create_game(client)
    game_id = game["game_id"]
    tile = game["hands"]["east"][0]

    with client.websocket_connect(_ws_url(game_id, "east")) as websocket:
        websocket.receive_json()
        websocket.receive_json()
        websocket.send_json({"type": "discard", "tile": tile})
        websocket.receive_json()  # state

        detail = client.get(f"/api/v1/games/{game_id}").json()
        assert detail["wall_remaining"] == 82
        assert detail["current_player"] == "south"
        expected_hand = list(game["hands"]["east"])
        expected_hand.remove(tile)
        assert detail["hands"]["east"] == expected_hand


def test_get_state只回复请求者(client: TestClient) -> None:
    """get_state 是单发：只回请求者，不广播给其他连接。"""
    game = _create_game(client)
    game_id = game["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as east:
        east.receive_json()
        east.receive_json()
        with client.websocket_connect(_ws_url(game_id, "south")) as south:
            south.receive_json()
            south.receive_json()
            east.receive_json()

            east.send_json({"type": "get_state"})
            state = east.receive_json()
            assert state["type"] == "state"
            assert state["wall_remaining"] == 83
            assert "last" not in state

            # 南家发 ping：如果 get_state 被错误广播，这里会先收到 state 而不是 pong
            south.send_json({"type": "ping", "ts": 1})
            assert south.receive_json() == {"type": "pong", "ts": 1}


def test_广播按对局隔离(client: TestClient) -> None:
    """两局同时在线：A 局打牌不会把 state 发给 B 局的连接。"""
    game_a = _create_game(client, seed=1)
    game_b = _create_game(client, seed=2)
    tile_a = game_a["hands"]["east"][0]

    with client.websocket_connect(_ws_url(game_a["game_id"], "east")) as ws_a:
        ws_a.receive_json()
        ws_a.receive_json()
        with client.websocket_connect(_ws_url(game_b["game_id"], "east")) as ws_b:
            ws_b.receive_json()
            ws_b.receive_json()

            ws_a.send_json({"type": "discard", "tile": tile_a})
            assert ws_a.receive_json()["type"] == "state"

            ws_b.send_json({"type": "ping", "ts": 7})
            assert ws_b.receive_json() == {"type": "pong", "ts": 7}


# --------------------------------------------------------------------------- #
# 三、心跳与错误处理
# --------------------------------------------------------------------------- #


def test_ping回pong并原样回显ts(client: TestClient) -> None:
    game_id = _create_game(client)["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as websocket:
        websocket.receive_json()
        websocket.receive_json()
        websocket.send_json({"type": "ping", "ts": 123456})
        assert websocket.receive_json() == {"type": "pong", "ts": 123456}


def test_非法JSON返回error但连接不断(client: TestClient) -> None:
    """发一段非 JSON：回 BAD_MESSAGE，随后 ping 仍能收到 pong（连接没被断）。"""
    game_id = _create_game(client)["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as websocket:
        websocket.receive_json()
        websocket.receive_json()

        websocket.send_text("这不是 JSON")
        error = websocket.receive_json()
        assert error["type"] == "error"
        assert error["code"] == "BAD_MESSAGE"

        websocket.send_json({"type": "ping", "ts": 1})
        assert websocket.receive_json() == {"type": "pong", "ts": 1}


def test_未知消息类型返回error但连接不断(client: TestClient) -> None:
    game_id = _create_game(client)["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as websocket:
        websocket.receive_json()
        websocket.receive_json()

        websocket.send_json({"type": "hack", "payload": "?"})
        error = websocket.receive_json()
        assert error["type"] == "error"
        assert error["code"] == "BAD_MESSAGE"
        assert "hack" in error["message"]

        websocket.send_json({"type": "ping", "ts": 2})
        assert websocket.receive_json() == {"type": "pong", "ts": 2}


def test_discard业务错误只回给发消息的人(client: TestClient) -> None:
    """打一张不在手里的牌：只有发送者收到 error，另一位玩家不会收到任何广播。"""
    game = _create_game(client)
    game_id = game["game_id"]
    east_hand = set(game["hands"]["east"])
    missing = next(tile for tile in TILE_KINDS if tile not in east_hand)

    with client.websocket_connect(_ws_url(game_id, "east")) as east:
        east.receive_json()
        east.receive_json()
        with client.websocket_connect(_ws_url(game_id, "south")) as south:
            south.receive_json()
            south.receive_json()
            east.receive_json()

            east.send_json({"type": "discard", "tile": missing})
            error = east.receive_json()
            assert error["type"] == "error"
            assert error["code"] == "TILE_NOT_IN_HAND"

            # 南家 ping 得到 pong → 说明错误没有变成广播
            south.send_json({"type": "ping", "ts": 3})
            assert south.receive_json() == {"type": "pong", "ts": 3}


def test_discard缺少tile字段返回BAD_MESSAGE(client: TestClient) -> None:
    game_id = _create_game(client)["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as websocket:
        websocket.receive_json()
        websocket.receive_json()
        websocket.send_json({"type": "discard"})
        error = websocket.receive_json()
        assert error["code"] == "BAD_MESSAGE"
        assert "tile" in error["message"]


# --------------------------------------------------------------------------- #
# 四、断线与在线状态
# --------------------------------------------------------------------------- #


def test_断线后presence更新且连接数归零(client: TestClient) -> None:
    """南家断开：东家收到只剩自己的 presence；全部断开后连接表清空。"""
    game_id = _create_game(client)["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as east:
        east.receive_json()
        east.receive_json()
        with client.websocket_connect(_ws_url(game_id, "south")) as south:
            south.receive_json()
            south.receive_json()
            east.receive_json()  # 南家加入
        # 南家上下文退出 → 东家收到离线后的 presence
        assert east.receive_json() == {"type": "presence", "seats_online": ["east"]}
        assert client.app.state.ws_manager.connection_count(game_id) == 1

    manager = client.app.state.ws_manager
    assert _wait_until(lambda: manager.connection_count(game_id) == 0)
    assert manager.seats_online(game_id) == []


def test_服务端空闲超时主动断开(client: TestClient, monkeypatch: pytest.MonkeyPatch) -> None:
    """把空闲超时调成 0.2s：客户端不发消息，服务端 close(1000)，客户端收到断开。"""
    monkeypatch.setattr(ws_module, "IDLE_TIMEOUT_SECONDS", 0.2)
    game_id = _create_game(client)["game_id"]
    with client.websocket_connect(_ws_url(game_id, "east")) as websocket:
        websocket.receive_json()
        websocket.receive_json()
        with pytest.raises(WebSocketDisconnect) as exc_info:
            websocket.receive_json()  # 阻塞直到服务端 close
    assert exc_info.value.code == 1000


# --------------------------------------------------------------------------- #
# 五、握手拒绝（close code 约定）
# --------------------------------------------------------------------------- #


def test_非法座位连接被拒4400(client: TestClient) -> None:
    game_id = _create_game(client)["game_id"]
    _expect_rejected(client, _ws_url(game_id, "east2"), 4400)


def test_不存在的对局连接被拒4404(client: TestClient) -> None:
    _expect_rejected(client, _ws_url("no-such-game", "east"), 4404)


def test_启用鉴权后无token被拒4401(auth_client: TestClient) -> None:
    token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers={"Authorization": f"Bearer {token}"})["game_id"]
    _expect_rejected(auth_client, _ws_url(game_id, "east"), 4401)


def test_启用鉴权后非法token被拒4401(auth_client: TestClient) -> None:
    token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers={"Authorization": f"Bearer {token}"})["game_id"]
    _expect_rejected(auth_client, _ws_url(game_id, "east", token="not-a-jwt"), 4401)


def test_启用鉴权后过期token被拒4401(auth_client: TestClient) -> None:
    token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers={"Authorization": f"Bearer {token}"})["game_id"]

    now = datetime.now(UTC)
    expired = pyjwt.encode(
        {
            "sub": "east",
            "type": "seat",
            "iat": now - timedelta(minutes=10),
            "exp": now - timedelta(minutes=5),
        },
        SECRET,
        algorithm="HS256",
    )
    _expect_rejected(auth_client, _ws_url(game_id, "east", token=expired), 4401)


def test_启用鉴权后token座位不匹配被拒4403(auth_client: TestClient) -> None:
    east_token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers={"Authorization": f"Bearer {east_token}"})["game_id"]
    south_token = _issue_token(auth_client, "south")
    # 拿南家的令牌去坐东家的位子 → 越权
    _expect_rejected(auth_client, _ws_url(game_id, "east", token=south_token), 4403)


def test_启用鉴权后合法token可连接(auth_client: TestClient) -> None:
    token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers={"Authorization": f"Bearer {token}"})["game_id"]
    with auth_client.websocket_connect(_ws_url(game_id, "east", token=token)) as websocket:
        connected = websocket.receive_json()
        assert connected["type"] == "connected"
        assert connected["seat"] == "east"
        assert websocket.receive_json()["seats_online"] == ["east"]
