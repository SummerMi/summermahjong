"""业务异常与统一异常处理器。

设计要点（面试可讲）：

1. 业务异常自带 ``code`` / ``status_code``，路由里 ``raise`` 即可，不需要每处手写
   ``HTTPException(status_code=..., detail=...)``，也不会把内部实现细节透给调用方。
2. 所有业务错误经 :func:`register_exception_handlers` 统一输出信封：

   .. code-block:: json

       {"error": {"code": "DIVISION_BY_ZERO", "message": "除数不能为 0"}}

3. 请求体格式错误（Pydantic 校验失败）**不**接管，保留 FastAPI 默认 422，
   这样前端能拿到字段级 loc / msg，便于表单回显。
"""

from __future__ import annotations

import logging

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

logger = logging.getLogger(__name__)


class AppError(Exception):
    """业务异常基类：一个异常对应「一个稳定错误码 + 一个 HTTP 状态码」。"""

    def __init__(self, code: str, message: str, status_code: int = 400) -> None:
        self.code = code
        self.message = message
        self.status_code = status_code
        super().__init__(message)


class InvalidHandError(AppError):
    """手牌非法：张数不对、出现未知牌、同种牌超过 4 张。"""

    def __init__(self, message: str) -> None:
        super().__init__("HAND_INVALID", message, status_code=400)


class GameNotFoundError(AppError):
    """对局不存在（或已被 TTL 清理 / 上限淘汰）。"""

    def __init__(self, game_id: str) -> None:
        super().__init__("GAME_NOT_FOUND", f"对局不存在：{game_id}", status_code=404)


class DivisionByZeroError(AppError):
    """计算器除数为 0。"""

    def __init__(self) -> None:
        super().__init__("DIVISION_BY_ZERO", "除数不能为 0", status_code=400)


class TileNotInHandError(AppError):
    """要打出的牌不在该座位手牌里。"""

    def __init__(self, tile: str) -> None:
        super().__init__("TILE_NOT_IN_HAND", f"手牌中没有 {tile}，无法打出", status_code=400)


def error_payload(code: str, message: str) -> dict[str, dict[str, str]]:
    """构造统一错误信封。"""
    return {"error": {"code": code, "message": message}}


async def app_error_handler(request: Request, exc: AppError) -> JSONResponse:
    """把业务异常渲染为标准错误信封。"""
    logger.info(
        "业务异常 method=%s path=%s code=%s message=%s",
        request.method,
        request.url.path,
        exc.code,
        exc.message,
    )
    return JSONResponse(status_code=exc.status_code, content=error_payload(exc.code, exc.message))


async def unhandled_error_handler(request: Request, exc: Exception) -> JSONResponse:
    """兜底异常：记录堆栈，对外只返回通用错误，避免泄漏内部细节。"""
    logger.exception("未捕获异常 method=%s path=%s", request.method, request.url.path)
    return JSONResponse(
        status_code=500,
        content=error_payload("INTERNAL_ERROR", "服务内部错误，请稍后重试"),
    )


def register_exception_handlers(app: FastAPI) -> None:
    """在应用上注册业务异常处理器。

    只注册 :class:`AppError` 与兜底 500；422 交给 FastAPI 默认实现。
    """
    app.add_exception_handler(AppError, app_error_handler)  # type: ignore[arg-type]
    app.add_exception_handler(Exception, unhandled_error_handler)
