"""WebSocket 实时对局路由：``/api/v1/ws/games/{game_id}``。

与 HTTP 接口共用同一个 :class:`~app.services.repository.base.GameRepository`，
所以「WS 打牌、HTTP 查询」看到的是同一份状态（测试里有跨连接一致性用例）。

握手（HTTP Upgrade）阶段的拒绝用 ``websocket.close(code=...)`` 表达，close code 约定：

==========  ====================================================
close code  含义
==========  ====================================================
4400        seat 查询参数非法（必须是 east / south / west / north）
4401        启用 JWT 时 token 缺失 / 无效 / 过期
4403        token 里的座位与 seat 参数不一致（座位越权）
4404        对局不存在（或已被 TTL 清理 / 上限淘汰）
1000        正常关闭；服务端空闲超时也用它（客户端可据此重连）
1011        应用未初始化等内部错误
==========  ====================================================

消息协议（服务端 → 客户端）：

- ``{"type": "connected", "game_id", "seat", "wall_remaining"}``：连接建立后的第一条
- ``{"type": "presence", "seats_online": [...]}``：连接加入 / 离开时广播给全桌
- ``{"type": "state", "wall_remaining", "current_player", "discards", "last"}``：
  有人打牌后广播；``get_state`` 的单发回复不带 ``last``
- ``{"type": "pong", "ts": <原样回显>}``：心跳响应
- ``{"type": "error", "code", "message"}``：只发给触发方，连接不因此断开

客户端 → 服务端：

- ``{"type": "ping", "ts": 123}``
- ``{"type": "discard", "tile": "2m"}``
- ``{"type": "get_state"}``

鉴权为什么用 ``?token=`` 查询参数：浏览器 WebSocket API **不能自定义请求头**，
没法像 HTTP 那样带 ``Authorization: Bearer``，所以主流做法是 query token 或
「连接后第一条消息再发 token」（本实现选前者，简单直观；注意 token 会出现在
访问日志里，生产要配日志脱敏、只收短期一次性 ticket）。
"""

from __future__ import annotations

import asyncio
import json
import logging
from typing import Any

from fastapi import APIRouter, WebSocket, WebSocketDisconnect

from app.core.errors import (
    AppError,
    AuthDisabledError,
    GameNotFoundError,
    TokenExpiredError,
    TokenInvalidError,
)
from app.core.security import decode_access_token
from app.services.realtime.manager import ConnectionManager
from app.services.repository.base import SEATS, GameRepository, GameState

logger = logging.getLogger(__name__)

router = APIRouter(tags=["实时对局"])

# ── WebSocket 关闭码约定（4000-4999 段留给应用自定义）──
CLOSE_SEAT_INVALID = 4400
CLOSE_UNAUTHORIZED = 4401
CLOSE_SEAT_MISMATCH = 4403
CLOSE_GAME_NOT_FOUND = 4404
CLOSE_INTERNAL_ERROR = 1011
CLOSE_NORMAL = 1000

IDLE_TIMEOUT_SECONDS = 60.0
"""服务端空闲超时（秒）：这么久没收到任何客户端消息就主动断开（close 1000）。

客户端侧已有 ping/pong 心跳；服务端这层是兜底：浏览器崩溃 / 断网 / 手机切后台时
TCP 连接可能“假死”（没有 FIN/RST），只靠 onclose 事件会永远不触发。
60s 允许客户端 30s 心跳丢一两次；测试里用 monkeypatch 把它调小来验证。
生产更严谨的做法是服务端周期发 ping 帧（``websocket.send_json`` 心跳）+
``asyncio.wait_for(pong)``，或依赖 ``uvicorn --ws-ping-interval``。
"""


def _get_manager(websocket: WebSocket) -> ConnectionManager:
    """取当前应用实例的连接管理器（首次连接时惰性创建）。

    不放在 ``main.py`` 里是因为本任务不改 ``main.py``，同时这样每个
    ``create_app()`` 实例都有独立连接表，测试之间天然隔离。
    """
    app = websocket.app
    manager: ConnectionManager | None = getattr(app.state, "ws_manager", None)
    if manager is None:
        manager = ConnectionManager()
        app.state.ws_manager = manager
    return manager


async def _reject(websocket: WebSocket, code: int, reason: str) -> None:
    """在 ``accept()`` 之前拒绝握手（客户端会收到 WebSocketDisconnect(code)）。"""
    logger.info("WS 握手被拒绝 code=%s reason=%s", code, reason)
    await websocket.close(code=code, reason=reason)


async def _safe_close(websocket: WebSocket, code: int, reason: str) -> None:
    """关闭连接；对端已经消失时忽略二次关闭的异常。"""
    try:
        await websocket.close(code=code, reason=reason)
    except (WebSocketDisconnect, RuntimeError) as exc:  # 连接已断 / 已发过 close 帧
        logger.info("WS 关闭连接时对端已消失：%r", exc)


async def _send(websocket: WebSocket, payload: dict[str, Any]) -> None:
    """发送一条 JSON 文本消息（不转义中文，方便抓包 / 日志阅读）。"""
    await websocket.send_text(json.dumps(payload, ensure_ascii=False))


async def _send_error(websocket: WebSocket, code: str, message: str) -> None:
    """只给触发方回一条错误消息；连接保持存活。"""
    await _send(websocket, {"type": "error", "code": code, "message": message})


def _state_payload(state: GameState, last: dict[str, str] | None = None) -> dict[str, Any]:
    """把对局快照组装成 ``state`` 消息；``last`` 表示刚刚有人打出的那张牌。"""
    payload: dict[str, Any] = {
        "type": "state",
        "wall_remaining": len(state.wall),
        "current_player": state.current_player,
        "discards": {seat: list(state.discards[seat]) for seat in SEATS},
    }
    if last is not None:
        payload["last"] = last
    return payload


async def _check_seat_token(websocket: WebSocket, seat: str) -> bool:
    """校验 ``?token=`` 查询参数；鉴权关闭时直接通过。

    :return: ``True`` 表示通过；``False`` 表示已拒绝握手（close 帧已发送）。
    """
    settings = websocket.app.state.settings
    if not settings.jwt_secret:
        # 与 HTTP 侧一致：JWT_SECRET 为空 = 鉴权整体关闭，token 参数可有可无
        return True

    token = websocket.query_params.get("token")
    if not token:
        await _reject(websocket, CLOSE_UNAUTHORIZED, "本实例已启用鉴权，必须带 ?token=<JWT>")
        return False

    try:
        token_seat = decode_access_token(token, settings)
    except TokenExpiredError:
        await _reject(websocket, CLOSE_UNAUTHORIZED, "token 已过期")
        return False
    except TokenInvalidError as exc:
        await _reject(websocket, CLOSE_UNAUTHORIZED, f"token 无效：{exc.message}")
        return False
    except AuthDisabledError:  # pragma: no cover - 上面已判断 jwt_secret 非空
        return True

    if token_seat != seat:
        await _reject(
            websocket,
            CLOSE_SEAT_MISMATCH,
            f"token 座位为 {token_seat}，与 seat={seat} 不一致",
        )
        return False
    return True


async def _handle_message(
    websocket: WebSocket,
    raw: str,
    game_id: str,
    seat: str,
    store: GameRepository,
    manager: ConnectionManager,
) -> None:
    """处理一条客户端消息。

    协议错误（非法 JSON / 未知 type / 缺字段）只回 ``error``，**不断线**：
    长连接里断线重连成本高，格式错误应可恢复。
    """
    try:
        message = json.loads(raw)
    except json.JSONDecodeError:
        await _send_error(websocket, "BAD_MESSAGE", "消息必须是合法 JSON")
        return
    if not isinstance(message, dict):
        await _send_error(websocket, "BAD_MESSAGE", "消息必须是 JSON 对象")
        return

    message_type = message.get("type")

    if message_type == "ping":
        # 心跳：原样回显 ts，客户端据此计算 RTT 判断链路是否可用
        await _send(websocket, {"type": "pong", "ts": message.get("ts")})
        return

    if message_type == "get_state":
        try:
            state = await store.get(game_id)
        except AppError as exc:
            await _send_error(websocket, exc.code, exc.message)
            return
        await _send(websocket, _state_payload(state))
        return

    if message_type == "discard":
        tile = message.get("tile")
        if not isinstance(tile, str) or not tile:
            await _send_error(websocket, "BAD_MESSAGE", "discard 消息必须带字符串字段 tile")
            return
        try:
            state = await store.discard(game_id, seat, tile)
        except AppError as exc:
            # 业务错误（牌不在手 / 没轮到你 / 对局没了）只回给发消息的人
            await _send_error(websocket, exc.code, exc.message)
            return
        # 成功后广播给全桌：所有人看到的牌河、牌墙、当前玩家保持一致
        await manager.broadcast(
            game_id,
            _state_payload(state, last={"seat": seat, "tile": tile}),
        )
        return

    await _send_error(
        websocket,
        "BAD_MESSAGE",
        f"未知消息类型：{message_type!r}（支持 ping / get_state / discard）",
    )


async def _receive_loop(
    websocket: WebSocket,
    game_id: str,
    seat: str,
    store: GameRepository,
    manager: ConnectionManager,
) -> None:
    """持续收消息，直到对端断开或服务端空闲超时。"""
    while True:
        try:
            raw = await asyncio.wait_for(
                websocket.receive_text(),
                timeout=IDLE_TIMEOUT_SECONDS,
            )
        except TimeoutError:
            logger.info("WS 空闲超时，主动断开 game_id=%s seat=%s", game_id, seat)
            await _safe_close(websocket, CLOSE_NORMAL, "idle timeout")
            return
        except WebSocketDisconnect:
            return
        await _handle_message(websocket, raw, game_id, seat, store, manager)


@router.websocket("/ws/games/{game_id}")
async def game_socket(websocket: WebSocket, game_id: str) -> None:
    """实时对局长连接：座位绑定 + 心跳 + 打牌广播 + 在线状态同步。

    握手校验顺序：seat 格式（4400）→ JWT（4401 / 4403）→ 对局存在（4404）。
    先验身份再查资源，避免未授权调用方用 close code 探测「对局是否存在」。
    """
    store: GameRepository | None = getattr(websocket.app.state, "store", None)
    if store is None or getattr(websocket.app.state, "settings", None) is None:
        # 只有绕过 lifespan 的异常启动方式才会走到这里
        await _safe_close(websocket, CLOSE_INTERNAL_ERROR, "应用尚未完成初始化")
        return

    seat = websocket.query_params.get("seat")
    if seat not in SEATS:
        await _reject(
            websocket,
            CLOSE_SEAT_INVALID,
            f"seat 必须是 {'/'.join(SEATS)}，收到 {seat!r}",
        )
        return

    if not await _check_seat_token(websocket, seat):
        return

    try:
        state = await store.get(game_id)
    except GameNotFoundError:
        await _reject(websocket, CLOSE_GAME_NOT_FOUND, f"对局不存在：{game_id}")
        return

    await websocket.accept()
    manager = _get_manager(websocket)
    await manager.connect(game_id, seat, websocket)

    try:
        # 第一条消息告诉客户端「你是谁、牌墙还剩多少」，便于前端初始化牌桌
        await _send(
            websocket,
            {
                "type": "connected",
                "game_id": game_id,
                "seat": seat,
                "wall_remaining": len(state.wall),
            },
        )
        # 广播在线座位：老连接看到新人加入，新连接也拿到完整名单
        await manager.broadcast(
            game_id,
            {"type": "presence", "seats_online": manager.seats_online(game_id)},
        )
        await _receive_loop(websocket, game_id, seat, store, manager)
    except WebSocketDisconnect:
        logger.info("WS 客户端断开 game_id=%s seat=%s", game_id, seat)
    except RuntimeError as exc:
        # 典型场景：空闲超时刚 close，同一轮里还有消息要发 / 收
        logger.info("WS 连接异常结束 game_id=%s seat=%s err=%r", game_id, seat, exc)
    finally:
        await manager.disconnect(game_id, websocket)
        if manager.connection_count(game_id) > 0:
            try:
                await manager.broadcast(
                    game_id,
                    {"type": "presence", "seats_online": manager.seats_online(game_id)},
                )
            except Exception as exc:  # noqa: BLE001 - 清理阶段不能因为广播失败而抛错
                logger.info("WS 离线广播失败 game_id=%s err=%r", game_id, exc)
