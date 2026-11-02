"""api 路由汇总：把各业务子路由挂到 ``/api/v1`` 前缀下。"""

from fastapi import APIRouter

from app.api.routes import auth, calc, games, mahjong

api_router = APIRouter(prefix="/api/v1")

api_router.include_router(auth.router, prefix="/auth")
api_router.include_router(calc.router, prefix="/calc")
api_router.include_router(mahjong.router, prefix="/mahjong")
api_router.include_router(games.router, prefix="/games")
