"""应用入口：``create_app()`` 工厂 + lifespan + CORS + 全局异常处理器。

同时提供模块级 ``app = create_app()``，让 ``uvicorn app.main:app`` 直接可跑；
工厂形态则方便测试注入自定义 ``Settings``（例如很小的 ``max_games``）。
"""

import logging
import time
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from sqlalchemy.ext.asyncio import AsyncEngine

from app.api.router import api_router
from app.api.routes.health import router as health_router
from app.core.config import Settings
from app.core.errors import register_exception_handlers
from app.services.repository.sql import (
    SqlGameRepository,
    create_engine_from_url,
    init_models,
    mask_dsn,
)
from app.services.store import GameStore

logger = logging.getLogger(__name__)

_DESCRIPTION = """
广东麻将（推倒胡）后端服务，用于演示一个「规则可解释、错误可预期、状态可复现」的
FastAPI 项目：

- **规则引擎**：胡牌判定（4 面子 + 1 将 / 七对）、听牌计算；
- **对局管理**：可切换的对局仓库（默认内存 asyncio 锁 + TTL；配置 `DATABASE_URL` 后切
  SQLAlchemy 异步关系库，SQLite 验证 / MySQL 生产），相同 seed 可复现牌局；
- **工程化**：Pydantic v2 校验、统一错误信封、依赖注入、pytest + ruff + Docker + CI。

错误统一返回 `{"error": {"code": "...", "message": "..."}}`；请求体格式错误保留 422。
""".strip()


def create_app(settings: Settings | None = None) -> FastAPI:
    """创建 FastAPI 应用。

    :param settings: 应用配置；省略时从环境变量 / ``.env`` 读取。
    """
    settings = settings or Settings()

    # 配置一次全局日志：业务代码统一用 logging.getLogger(__name__)，不用 print
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s | %(levelname)-7s | %(name)s | %(message)s",
    )

    @asynccontextmanager
    async def lifespan(application: FastAPI) -> AsyncIterator[None]:
        """启动时按配置选择存储实现，关闭时释放资源。

        ``settings.database_url`` 为空 → 内存 ``GameStore``（零依赖，测试 / demo 默认）；
        非空 → 建异步 engine + ``create_all`` 建表，用 ``SqlGameRepository``。
        关闭时内存实现清空状态，SQL 实现只 ``dispose`` 连接池、**保留数据**
        （所以重启后 game_id 仍然查得到）。
        """
        engine: AsyncEngine | None = None
        if settings.database_url:
            engine = create_engine_from_url(settings.database_url, echo=settings.db_echo)
            await init_models(engine)
            application.state.store = SqlGameRepository(
                engine,
                max_games=settings.max_games,
                ttl_seconds=settings.game_ttl_seconds,
            )
            storage = f"sql({mask_dsn(settings.database_url)})"
        else:
            application.state.store = GameStore(
                max_games=settings.max_games,
                ttl_seconds=settings.game_ttl_seconds,
            )
            storage = "memory"
        application.state.db_engine = engine
        application.state.started_at = time.monotonic()
        logger.info(
            "服务启动 name=%s version=%s storage=%s max_games=%d ttl=%ds",
            settings.app_name,
            settings.version,
            storage,
            settings.max_games,
            settings.game_ttl_seconds,
        )
        try:
            yield
        finally:
            if engine is not None:
                await engine.dispose()
                logger.info("服务关闭，数据库连接池已释放（对局数据持久化保留）")
            else:
                await application.state.store.clear()
                logger.info("服务关闭，内存对局已清空")

    application = FastAPI(
        title=settings.app_name,
        version=settings.version,
        description=_DESCRIPTION,
        docs_url="/docs",
        redoc_url="/redoc",
        openapi_url="/openapi.json",
        lifespan=lifespan,
    )

    # 先放一份 settings / started_at：即使没走 lifespan（如某些单测直接调用），
    # 根路径与 /healthz 也能正常工作；store 仍然只在 lifespan 中创建以体现生命周期。
    application.state.settings = settings
    application.state.started_at = time.monotonic()

    # CORS：demo 默认允许所有来源。生产环境务必把 CORS_ORIGINS 收紧成具体域名，
    # 且 allow_credentials 与通配来源不能同时为 True（浏览器会拒绝）。
    application.add_middleware(
        CORSMiddleware,
        allow_origins=settings.cors_origin_list,
        allow_credentials=False,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    register_exception_handlers(application)

    application.include_router(health_router)
    application.include_router(api_router)

    @application.get("/", tags=["元信息"], summary="服务简介")
    async def read_root() -> dict[str, str]:
        """浏览器打开的第一屏：项目名称、版本、文档入口。"""
        return {
            "name": settings.app_name,
            "version": settings.version,
            "description": "广东麻将（推倒胡）后端服务：规则判定 + 听牌计算 + 对局管理",
            "docs": "/docs",
            "redoc": "/redoc",
            "openapi": "/openapi.json",
            "health": "/healthz",
        }

    return application


app = create_app()
