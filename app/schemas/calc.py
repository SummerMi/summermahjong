"""计算器相关的请求 / 响应模型。

``a`` / ``b`` 用 ``int | float`` 而不是纯 ``float``：
整数入参时结果保持整数（``1 + 2 -> 3``），便于演示；除法结果自然变成浮点数。
"""

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

CalcOp = Literal["add", "subtract", "multiply", "divide"]
"""支持的运算类型；未知运算由 FastAPI 路径参数校验直接返回 422。"""


class CalcRequest(BaseModel):
    """四则运算的请求体。"""

    model_config = ConfigDict(json_schema_extra={"examples": [{"a": 1, "b": 2}]})

    a: int | float = Field(..., description="操作数 a", examples=[1])
    b: int | float = Field(..., description="操作数 b", examples=[2])


class CalcResponse(BaseModel):
    """四则运算的响应体，回显 op 与入参，便于前端核对。"""

    op: CalcOp = Field(..., description="本次执行的运算", examples=["add"])
    a: int | float = Field(..., description="操作数 a", examples=[1])
    b: int | float = Field(..., description="操作数 b", examples=[2])
    result: int | float = Field(..., description="运算结果；除法结果为浮点数", examples=[3])
