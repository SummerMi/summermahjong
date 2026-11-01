"""应用层基础端点测试：根路径、健康检查、计算器、CORS。

覆盖成功 / 400 业务错误 / 422 格式错误三类语义，并断言错误统一信封。
"""

from collections.abc import Iterator

import pytest
from fastapi.testclient import TestClient

from app.core.config import Settings
from app.main import create_app


@pytest.fixture()
def client() -> Iterator[TestClient]:
    """每个用例一个独立应用实例（独立 lifespan、独立内存对局仓库）。"""
    with TestClient(create_app()) as test_client:
        yield test_client


def test_配置支持环境变量覆盖(monkeypatch: pytest.MonkeyPatch) -> None:
    """pydantic-settings 读环境变量：MAX_GAMES / APP_NAME / CORS_ORIGINS 都能覆盖。"""
    monkeypatch.setenv("MAX_GAMES", "3")
    monkeypatch.setenv("APP_NAME", "测试用麻将服务")
    monkeypatch.setenv("CORS_ORIGINS", "http://localhost:5173, https://demo.example.com")

    settings = Settings()
    assert settings.max_games == 3
    assert settings.app_name == "测试用麻将服务"
    assert settings.cors_origin_list == ["http://localhost:5173", "https://demo.example.com"]


def test_根路径返回项目简介与文档链接(client: TestClient) -> None:
    """浏览器第一屏：名称、版本、/docs 入口都要有。"""
    response = client.get("/")
    assert response.status_code == 200
    data = response.json()
    assert "麻将" in data["name"]
    assert data["version"] == "0.1.0"
    assert data["docs"] == "/docs"
    assert data["health"] == "/healthz"


def test_健康检查返回ok与运行时长(client: TestClient) -> None:
    """/healthz 供 Docker HEALTHCHECK / 负载均衡探针使用。"""
    response = client.get("/healthz")
    assert response.status_code == 200
    data = response.json()
    assert data["status"] == "ok"
    assert data["version"] == "0.1.0"
    assert data["uptime_seconds"] >= 0


@pytest.mark.parametrize(
    ("op", "a", "b", "expected"),
    [
        ("add", 1, 2, 3),
        ("subtract", 10, 4, 6),
        ("multiply", 3, 7, 21),
        ("divide", 9, 3, 3.0),
    ],
)
def test_四则运算返回正确结果(
    client: TestClient, op: str, a: int, b: int, expected: float
) -> None:
    """四种运算都要回显 op / a / b 并给出正确结果。"""
    response = client.post(f"/api/v1/calc/{op}", json={"a": a, "b": b})
    assert response.status_code == 200
    data = response.json()
    assert data["op"] == op
    assert data["a"] == a
    assert data["b"] == b
    assert data["result"] == expected


@pytest.mark.parametrize(
    ("op", "a", "b", "expected"),
    [
        ("add", -1.5, 2, 0.5),
        ("subtract", 0.5, 1.5, -1.0),
        ("multiply", -1.5, 2, -3.0),
        ("divide", 2.5, 0.5, 5.0),
        ("add", 0.1, 0.2, pytest.approx(0.3)),
    ],
)
def test_支持小数与负数(
    client: TestClient, op: str, a: float, b: float, expected: float
) -> None:
    """入参支持小数、负数；浮点结果按 approx 比较。"""
    response = client.post(f"/api/v1/calc/{op}", json={"a": a, "b": b})
    assert response.status_code == 200
    assert response.json()["result"] == expected


def test_除数为零返回400统一信封(client: TestClient) -> None:
    """除数为 0 是业务错误：400 + {"error": {"code", "message"}}。"""
    response = client.post("/api/v1/calc/divide", json={"a": 1, "b": 0})
    assert response.status_code == 400
    assert response.json() == {
        "error": {"code": "DIVISION_BY_ZERO", "message": "除数不能为 0"}
    }


def test_未知运算返回422(client: TestClient) -> None:
    """未知 op 属于请求格式问题，交给 FastAPI 默认 422。"""
    response = client.post("/api/v1/calc/mod", json={"a": 1, "b": 2})
    assert response.status_code == 422
    assert "detail" in response.json()


def test_请求体类型错误返回422(client: TestClient) -> None:
    """a 传字符串无法转成数字 → 422。"""
    response = client.post("/api/v1/calc/add", json={"a": "一", "b": 2})
    assert response.status_code == 422


def test_请求体缺少字段返回422(client: TestClient) -> None:
    """缺少必填字段 b → 422。"""
    response = client.post("/api/v1/calc/add", json={"a": 1})
    assert response.status_code == 422


def test_跨域预检返回允许来源(client: TestClient) -> None:
    """demo 允许所有来源；生产应把 CORS_ORIGINS 收紧成具体域名。"""
    response = client.options(
        "/healthz",
        headers={
            "Origin": "http://localhost:5173",
            "Access-Control-Request-Method": "GET",
        },
    )
    assert response.status_code == 200
    assert response.headers["access-control-allow-origin"] == "*"
