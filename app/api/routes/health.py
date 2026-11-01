"""健康检查路由（部署探针 / Docker HEALTHCHECK / 负载均衡用）。

``/healthz`` 不在 ``/api/v1`` 前缀下：健康检查属于运维接口，不参与业务版本演进。
"""

import time

from fastapi import APIRouter, Request
from pydantic import BaseModel, Field

from app.api.deps import SettingsDep

router = APIRouter(tags=["健康检查"])


class HealthResponse(BaseModel):
    """健康检查响应。"""

    status: str = Field(..., description="服务状态，ok 表示可用", examples=["ok"])
    version: str = Field(..., description="服务版本号", examples=["0.1.0"])
    uptime_seconds: float = Field(..., description="进程已运行秒数", examples=[123.4])


@router.get("/healthz", response_model=HealthResponse, summary="健康检查")
async def healthz(request: Request, settings: SettingsDep) -> HealthResponse:
    """返回服务状态、版本与运行时长。"""
    started_at = getattr(request.app.state, "started_at", None)
    uptime = time.monotonic() - started_at if started_at is not None else 0.0
    return HealthResponse(
        status="ok",
        version=settings.version,
        uptime_seconds=round(uptime, 3),
    )
