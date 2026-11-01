"""麻将规则端点测试：牌墙清单、胡牌判定、听牌计算。

这里调用**真实引擎**（app.services.mahjong），不 mock：
规则判定是这个项目的核心卖点，必须端到端验证。
"""

from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient

from app.main import create_app

# 14 张标准胡牌：123m / 456m / 789m / 111p / EE（4 面子 + 1 将）
WIN_STANDARD_14 = [
    "1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "1p", "E", "E",
]
# 14 张七对：1m2m3m4p5s + E P 各一对
WIN_SEVEN_PAIRS_14 = [
    "1m", "1m", "2m", "2m", "3m", "3m", "4p", "4p", "5s", "5s", "E", "E", "P", "P",
]
# 14 张但不成型（4 个刻子 + 单张 5m、7m）
NOT_WIN_14 = [
    "1m", "1m", "1m", "2m", "2m", "2m", "3m", "3m", "3m", "4m", "4m", "4m", "5m", "7m",
]
# 13 张听 E：123m / 456m / 789m / 111p + 单张 E
TENPAI_EAST_13 = [
    "1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "1p", "E",
]
# 13 张五面听：111m / 222m / 333m / 444m + 5m（听 2m 3m 4m 5m 6m）
TENPAI_MULTI_13 = [
    "1m", "1m", "1m", "2m", "2m", "2m", "3m", "3m", "3m", "4m", "4m", "4m", "5m",
]
# 13 张完全散牌，不听牌
NOT_TENPAI_13 = [
    "1m", "4m", "7m", "1p", "4p", "7p", "1s", "4s", "7s", "E", "S", "W", "N",
]


@pytest.fixture()
def client() -> Iterator[TestClient]:
    """每个用例一个独立应用实例。"""
    with TestClient(create_app()) as test_client:
        yield test_client


def test_全副牌返回136张34种(client: TestClient) -> None:
    """total / kinds 与 34 种牌型清单要自洽。"""
    response = client.get("/api/v1/mahjong/tiles")
    assert response.status_code == 200
    data = response.json()
    assert data["total"] == 136
    assert data["kinds"] == 34
    assert len(data["tiles"]) == 34
    assert len(set(data["tiles"])) == 34
    assert data["tiles"][0] == "1m"
    assert data["tiles"][-1] == "C"


def test_标准胡牌判定为standard(client: TestClient) -> None:
    """4 面子 + 1 将 → is_win=true, pattern=standard。"""
    response = client.post("/api/v1/mahjong/win-check", json={"hand": WIN_STANDARD_14})
    assert response.status_code == 200
    assert response.json() == {"is_win": True, "pattern": "standard"}


def test_七对胡牌判定为seven_pairs(client: TestClient) -> None:
    """七对 → is_win=true, pattern=seven_pairs。"""
    response = client.post(
        "/api/v1/mahjong/win-check", json={"hand": WIN_SEVEN_PAIRS_14}
    )
    assert response.status_code == 200
    assert response.json() == {"is_win": True, "pattern": "seven_pairs"}


def test_非胡牌返回未胡(client: TestClient) -> None:
    """牌型不成立时 is_win=false, pattern=null。"""
    response = client.post("/api/v1/mahjong/win-check", json={"hand": NOT_WIN_14})
    assert response.status_code == 200
    assert response.json() == {"is_win": False, "pattern": None}


def test_十三张手牌直接判未胡(client: TestClient) -> None:
    """13 张尚未摸牌（少一张将），直接返回未胡，便于前端复用同一接口。"""
    response = client.post(
        "/api/v1/mahjong/win-check", json={"hand": TENPAI_EAST_13}
    )
    assert response.status_code == 200
    assert response.json() == {"is_win": False, "pattern": None}


def test_手牌张数非法返回400(client: TestClient) -> None:
    """张数不在 13/14 → 400 HAND_INVALID，而不是 422。"""
    response = client.post("/api/v1/mahjong/win-check", json={"hand": ["1m", "2m"]})
    assert response.status_code == 400
    body = response.json()
    assert body["error"]["code"] == "HAND_INVALID"
    assert "13 或 14" in body["error"]["message"]


def test_手牌包含未知牌返回400(client: TestClient) -> None:
    """9q 不是合法牌面 → 400 HAND_INVALID。"""
    hand = [*WIN_STANDARD_14[:-1], "9q"]
    response = client.post("/api/v1/mahjong/win-check", json={"hand": hand})
    assert response.status_code == 400
    body = response.json()
    assert body["error"]["code"] == "HAND_INVALID"
    assert "9q" in body["error"]["message"]


def test_同种牌超过四张返回400(client: TestClient) -> None:
    """一副牌只有 4 张 1m，第 5 张非法。"""
    hand = ["1m", "1m", "1m", "1m", "1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p"]
    response = client.post("/api/v1/mahjong/win-check", json={"hand": hand})
    assert response.status_code == 400
    assert response.json()["error"]["code"] == "HAND_INVALID"


def test_手牌格式错误返回422(client: TestClient) -> None:
    """hand 不是字符串数组属于格式问题 → 422。"""
    response = client.post("/api/v1/mahjong/win-check", json={"hand": "1m2m3m"})
    assert response.status_code == 422


def test_听牌返回可胡牌种(client: TestClient) -> None:
    """13 张手牌听 E。"""
    response = client.post("/api/v1/mahjong/tenpai", json={"hand": TENPAI_EAST_13})
    assert response.status_code == 200
    assert response.json() == {"waiting_tiles": ["E"]}


def test_多面听返回升序牌种(client: TestClient) -> None:
    """111m222m333m444m+5m 听 2m/3m/4m/5m/6m，按牌序升序返回。"""
    response = client.post("/api/v1/mahjong/tenpai", json={"hand": TENPAI_MULTI_13})
    assert response.status_code == 200
    assert response.json() == {"waiting_tiles": ["2m", "3m", "4m", "5m", "6m"]}


def test_不听牌返回空列表(client: TestClient) -> None:
    """散牌不听牌时返回空列表，而不是 404 / null。"""
    response = client.post("/api/v1/mahjong/tenpai", json={"hand": NOT_TENPAI_13})
    assert response.status_code == 200
    assert response.json() == {"waiting_tiles": []}


def test_听牌张数不对返回400(client: TestClient) -> None:
    """听牌计算必须正好 13 张。"""
    response = client.post("/api/v1/mahjong/tenpai", json={"hand": WIN_STANDARD_14})
    assert response.status_code == 400
    body = response.json()
    assert body["error"]["code"] == "HAND_INVALID"
    assert "13" in body["error"]["message"]


def test_听牌含未知牌返回400(client: TestClient) -> None:
    """13 张里混入非法牌面 → 400 HAND_INVALID。"""
    hand = [*TENPAI_EAST_13[:-1], "0z"]
    response = client.post("/api/v1/mahjong/tenpai", json={"hand": hand})
    assert response.status_code == 400
    assert response.json()["error"]["code"] == "HAND_INVALID"
