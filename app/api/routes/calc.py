"""计算器路由：从旧学习脚本的 hello / add / subtract / divide 玩具路由升级而来。

演示两个面试常问点：

- 路径参数用 ``Literal`` 收敛取值，未知运算由 FastAPI 自动返回 **422**；
- 业务错误（除数为 0）抛 :class:`DivisionByZeroError`，由全局处理器转成
  **400 + 统一信封**，路由里不出现 ``HTTPException``。
"""

import operator
from collections.abc import Callable

from fastapi import APIRouter

from app.core.errors import DivisionByZeroError
from app.schemas.calc import CalcOp, CalcRequest, CalcResponse

router = APIRouter(tags=["计算器"])

_OPERATIONS: dict[CalcOp, Callable[[float, float], float]] = {
    "add": operator.add,
    "subtract": operator.sub,
    "multiply": operator.mul,
    "divide": operator.truediv,
}


@router.post(
    "/{op}",
    response_model=CalcResponse,
    summary="四则运算",
    description="op ∈ add / subtract / multiply / divide；除数为 0 返回 400（DIVISION_BY_ZERO）。",
)
async def calculate(op: CalcOp, payload: CalcRequest) -> CalcResponse:
    """执行一次四则运算并回显入参。"""
    if op == "divide" and payload.b == 0:
        raise DivisionByZeroError()
    result = _OPERATIONS[op](payload.a, payload.b)
    return CalcResponse(op=op, a=payload.a, b=payload.b, result=result)
