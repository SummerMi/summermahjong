"""广东麻将（推倒胡）后端服务 —— FastAPI 应用层。

分层约定：

- ``app.core``     ：配置与业务异常（与框架无关的横切能力）
- ``app.schemas``  ：Pydantic 请求 / 响应模型，只做「格式」校验
- ``app.api``      ：路由、依赖注入、异常到 HTTP 的映射
- ``app.services`` ：内存对局仓库（可替换为 Redis/MySQL）与麻将算法引擎
"""

__version__ = "0.1.0"
