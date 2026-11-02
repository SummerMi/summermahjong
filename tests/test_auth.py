"""JWT 鉴权 + 座位绑定测试。

约定：

- **鉴权关闭**（``JWT_SECRET`` 为空）是 demo / CI 默认，必须零行为变化；
- **鉴权开启** 用 ``create_app(Settings(jwt_secret=...))`` 注入，不碰全局 ``.env``；
- 过期 / 错签名 / 错用途 / 非法座位的令牌都在测试里手工签发（PyJWT），
  不 sleep、不依赖真实时间流逝，保证测试稳定可复跑。
"""

from collections.abc import Iterator
from datetime import UTC, datetime, timedelta
from typing import Any

import jwt as pyjwt
import pytest
from fastapi.testclient import TestClient

from app.core.config import Settings
from app.core.security import decode_access_token
from app.main import create_app

SECRET = "test-secret-for-pytest-at-least-32-bytes"  # 测试专用密钥，绝不用于真实环境
OTHER_SECRET = "another-secret-at-least-32-bytes-long"  # 用来构造“签名错误”的令牌


@pytest.fixture()
def open_client() -> Iterator[TestClient]:
    """鉴权关闭（默认）的应用：行为必须与引入 JWT 之前完全一致。"""
    with TestClient(create_app(Settings(jwt_secret=""))) as client:
        yield client


@pytest.fixture()
def auth_client() -> Iterator[TestClient]:
    """鉴权开启的应用：独立密钥 + 60 分钟有效期。"""
    with TestClient(create_app(Settings(jwt_secret=SECRET, jwt_expire_minutes=60))) as client:
        yield client


def _bearer(token: str) -> dict[str, str]:
    """构造 ``Authorization: Bearer`` 请求头。"""
    return {"Authorization": f"Bearer {token}"}


def _issue_token(client: TestClient, seat: str = "east") -> str:
    """调用 /auth/token 拿一枚合法令牌。"""
    response = client.post("/api/v1/auth/token", json={"seat": seat})
    assert response.status_code == 200, response.text
    return response.json()["access_token"]


def _encode_claims(
    *,
    secret: str = SECRET,
    **overrides: Any,
) -> str:
    """用测试密钥手工签一枚令牌，用来构造异常场景（过期 / 错用途 / 非法座位）。"""
    now = datetime.now(UTC)
    claims: dict[str, Any] = {
        "sub": "east",
        "type": "seat",
        "iat": now,
        "exp": now + timedelta(minutes=5),
    }
    claims.update(overrides)
    return pyjwt.encode(claims, secret, algorithm="HS256")


def _create_game(
    client: TestClient,
    headers: dict[str, str] | None = None,
    seed: int = 42,
) -> dict[str, Any]:
    """创建一局并断言成功，返回响应体。"""
    response = client.post("/api/v1/games", json={"seed": seed}, headers=headers)
    assert response.status_code == 200, response.text
    return response.json()


# --------------------------------------------------------------------------- #
# 一、鉴权关闭：零行为变化（老接口原样可用）
# --------------------------------------------------------------------------- #


def test_鉴权关闭时不需要令牌也能建局查询打牌(open_client: TestClient) -> None:
    """默认实例没有 JWT_SECRET：建局 / 查询 / 打牌全部不需要 token。"""
    created = _create_game(open_client)
    # 老行为：四家手牌都是完整数组
    assert all(isinstance(hand, list) for hand in created["hands"].values())
    assert len(created["hands"]["east"]) == 14

    game_id = created["game_id"]
    detail = open_client.get(f"/api/v1/games/{game_id}")
    assert detail.status_code == 200
    assert len(detail.json()["hands"]["east"]) == 14
    assert detail.json()["hands"]["south"] == 13

    response = open_client.post(
        f"/api/v1/games/{game_id}/discard",
        json={"seat": "east", "tile": created["hands"]["east"][0]},
    )
    assert response.status_code == 200
    assert response.json()["next_player"] == "south"


def test_鉴权关闭时令牌接口返回503(open_client: TestClient) -> None:
    """没开鉴权就别发令牌：503 AUTH_DISABLED，并在消息里点名 JWT_SECRET。"""
    response = open_client.post("/api/v1/auth/token", json={"seat": "east"})
    assert response.status_code == 503
    body = response.json()
    assert body["error"]["code"] == "AUTH_DISABLED"
    assert "JWT_SECRET" in body["error"]["message"]


def test_鉴权关闭时乱写的Authorization头被忽略(open_client: TestClient) -> None:
    """关闭鉴权时完全不读 Authorization，带垃圾令牌也不影响业务（老行为）。"""
    response = open_client.post(
        "/api/v1/games",
        json={"seed": 1},
        headers={"Authorization": "Bearer definitely-not-a-jwt"},
    )
    assert response.status_code == 200
    assert len(response.json()["hands"]["east"]) == 14


# --------------------------------------------------------------------------- #
# 二、鉴权开启：令牌校验的 401 矩阵
# --------------------------------------------------------------------------- #


def test_开启鉴权后缺少令牌返回401(auth_client: TestClient) -> None:
    """没有任何 Authorization 头 → 401 TOKEN_MISSING（并带 WWW-Authenticate）。"""
    response = auth_client.post("/api/v1/games", json={"seed": 42})
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_MISSING"
    assert response.headers["www-authenticate"].startswith("Bearer")


def test_开启鉴权后格式错误的令牌返回401(auth_client: TestClient) -> None:
    """不是三段式 JWT 的字符串 → 401 TOKEN_INVALID。"""
    response = auth_client.post(
        "/api/v1/games", json={"seed": 42}, headers=_bearer("not-a-jwt")
    )
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_INVALID"


def test_开启鉴权后签名错误的令牌返回401(auth_client: TestClient) -> None:
    """用别的密钥签的令牌（签名对不上）→ 401 TOKEN_INVALID，绝不能放行。"""
    forged = _encode_claims(secret=OTHER_SECRET)
    response = auth_client.post("/api/v1/games", json={"seed": 42}, headers=_bearer(forged))
    assert response.status_code == 401
    body = response.json()
    assert body["error"]["code"] == "TOKEN_INVALID"
    assert "签名" in body["error"]["message"] or "格式" in body["error"]["message"]


def test_开启鉴权后过期令牌返回401(auth_client: TestClient) -> None:
    """exp 已过期的令牌 → 401 TOKEN_EXPIRED（错误码要能和“无效”区分开）。"""
    expired = _encode_claims(exp=datetime.now(UTC) - timedelta(seconds=1))
    response = auth_client.post("/api/v1/games", json={"seed": 42}, headers=_bearer(expired))
    assert response.status_code == 401
    body = response.json()
    assert body["error"]["code"] == "TOKEN_EXPIRED"
    assert "过期" in body["error"]["message"]


def test_开启鉴权后用途不对的令牌返回401(auth_client: TestClient) -> None:
    """type=refresh 的令牌不能当座位令牌用 → 401 TOKEN_INVALID。"""
    wrong_type = _encode_claims(type="refresh")
    response = auth_client.post("/api/v1/games", json={"seed": 42}, headers=_bearer(wrong_type))
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_INVALID"


def test_开启鉴权后非法座位的令牌返回401(auth_client: TestClient) -> None:
    """签名正确但 sub 不是合法座位 → 401 TOKEN_INVALID。"""
    bad_seat = _encode_claims(sub="admin")
    response = auth_client.post("/api/v1/games", json={"seed": 42}, headers=_bearer(bad_seat))
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_INVALID"


def test_开启鉴权后认证方案不是Bearer返回401(auth_client: TestClient) -> None:
    """Basic / 裸 token 等格式 → 401 TOKEN_INVALID（不是“缺少令牌”）。"""
    response = auth_client.get(
        "/api/v1/games/some-id", headers={"Authorization": "Basic dXNlcjpwYXNz"}
    )
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_INVALID"


# --------------------------------------------------------------------------- #
# 三、令牌签发接口
# --------------------------------------------------------------------------- #


def test_令牌接口返回bearer令牌与有效期(auth_client: TestClient) -> None:
    """/auth/token 返回 OAuth2 风格字段，且令牌能被服务端解出座位。"""
    response = auth_client.post("/api/v1/auth/token", json={"seat": "south"})
    assert response.status_code == 200
    body = response.json()
    assert body["token_type"] == "bearer"
    assert body["expires_in"] == 60 * 60  # jwt_expire_minutes=60 注入
    assert decode_access_token(
        body["access_token"], Settings(jwt_secret=SECRET)
    ) == "south"


def test_令牌接口座位非法返回422(auth_client: TestClient) -> None:
    """座位不是枚举内取值属于请求格式问题 → 422（不是 401）。"""
    response = auth_client.post("/api/v1/auth/token", json={"seat": "east2"})
    assert response.status_code == 422


# --------------------------------------------------------------------------- #
# 四、座位绑定：建局 / 查询只看得见自己
# --------------------------------------------------------------------------- #


def test_合法令牌建局成功且只看到自己手牌(auth_client: TestClient) -> None:
    """南家建局：south 是完整数组，其余座位只给张数。"""
    token = _issue_token(auth_client, "south")
    created = _create_game(auth_client, headers=_bearer(token))
    assert isinstance(created["hands"]["south"], list)
    assert len(created["hands"]["south"]) == 13
    for seat in ("east", "west", "north"):
        assert isinstance(created["hands"][seat], int)
    assert created["hands"]["east"] == 14  # 庄家 14 张，但只是张数


def test_不同座位各自只看自己的手牌(auth_client: TestClient) -> None:
    """同一局里，东家只看到东家手牌、南家只看到南家手牌（互相看不到）。"""
    east_token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers=_bearer(east_token))["game_id"]
    south_token = _issue_token(auth_client, "south")

    east_view = auth_client.get(f"/api/v1/games/{game_id}", headers=_bearer(east_token)).json()
    assert len(east_view["hands"]["east"]) == 14
    assert east_view["hands"]["south"] == 13

    south_view = auth_client.get(f"/api/v1/games/{game_id}", headers=_bearer(south_token)).json()
    assert len(south_view["hands"]["south"]) == 13
    assert south_view["hands"]["east"] == 14


def test_查询对局无令牌返回401(auth_client: TestClient) -> None:
    """查询接口也要令牌：不带 token 的 GET 直接 401。"""
    token = _issue_token(auth_client)
    game_id = _create_game(auth_client, headers=_bearer(token))["game_id"]
    response = auth_client.get(f"/api/v1/games/{game_id}")
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_MISSING"


def test_查询不存在对局带合法令牌返回404(auth_client: TestClient) -> None:
    """先过鉴权，再查资源：合法令牌访问不存在的对局仍然是 404。"""
    token = _issue_token(auth_client)
    response = auth_client.get("/api/v1/games/no-such-game", headers=_bearer(token))
    assert response.status_code == 404
    assert response.json()["error"]["code"] == "GAME_NOT_FOUND"


# --------------------------------------------------------------------------- #
# 五、座位绑定：打牌越权
# --------------------------------------------------------------------------- #


def test_打牌座位不匹配返回403(auth_client: TestClient) -> None:
    """东家令牌想替南家打牌 → 403 SEAT_MISMATCH，且牌局状态不变。"""
    token = _issue_token(auth_client, "east")
    created = _create_game(auth_client, headers=_bearer(token))
    response = auth_client.post(
        f"/api/v1/games/{created['game_id']}/discard",
        json={"seat": "south", "tile": "1m"},
        headers=_bearer(token),
    )
    assert response.status_code == 403
    body = response.json()
    assert body["error"]["code"] == "SEAT_MISMATCH"
    assert "east" in body["error"]["message"] and "south" in body["error"]["message"]

    detail = auth_client.get(
        f"/api/v1/games/{created['game_id']}", headers=_bearer(token)
    ).json()
    assert detail["wall_remaining"] == 83  # 越权请求没有改变任何状态


def test_打牌座位匹配成功(auth_client: TestClient) -> None:
    """东家令牌以东家身份打自己的牌 → 200，牌进牌河、轮转南家。"""
    token = _issue_token(auth_client, "east")
    created = _create_game(auth_client, headers=_bearer(token))
    tile = created["hands"]["east"][0]
    response = auth_client.post(
        f"/api/v1/games/{created['game_id']}/discard",
        json={"seat": "east", "tile": tile},
        headers=_bearer(token),
    )
    assert response.status_code == 200
    body = response.json()
    assert body["discards"]["east"] == [tile]
    assert body["next_player"] == "south"
    assert body["wall_remaining"] == 82


def test_打牌无令牌返回401(auth_client: TestClient) -> None:
    """打牌接口同样要求令牌。"""
    token = _issue_token(auth_client, "east")
    game_id = _create_game(auth_client, headers=_bearer(token))["game_id"]
    response = auth_client.post(
        f"/api/v1/games/{game_id}/discard", json={"seat": "east", "tile": "1m"}
    )
    assert response.status_code == 401
    assert response.json()["error"]["code"] == "TOKEN_MISSING"


def test_座位匹配但没轮到自己出牌返回400(auth_client: TestClient) -> None:
    """座位绑定只解决“你是谁”，回合顺序仍由业务规则管：南家抢先打 → 400。"""
    token = _issue_token(auth_client, "south")
    created = _create_game(auth_client, headers=_bearer(token))
    response = auth_client.post(
        f"/api/v1/games/{created['game_id']}/discard",
        json={"seat": "south", "tile": "1m"},
        headers=_bearer(token),
    )
    assert response.status_code == 400
    assert response.json()["error"]["code"] == "NOT_YOUR_TURN"
