"""按对局维护 WebSocket 连接，并提供「不互相拖累」的广播能力。

设计要点（面试可讲）：

1. **单进程内存连接表**：``dict[game_id, list[Connection]]``。连接只对本进程可见，
   所以多实例部署时「A 实例的玩家打牌、B 实例的玩家收不到广播」——生产必须换成
   Redis pub/sub / Stream/Kafka 做跨实例扇出（见 ``docs/adr-0005-websocket实时对局.md``）。
2. **一把 asyncio.Lock 保护集合**：``connect`` / ``disconnect`` / ``broadcast`` 的
   增删都在锁内完成；广播时**先在锁内拍快照**（``list(...)``），再在锁外逐条发送，
   避免「发送慢就把整个连接表锁住」，也避免遍历时被并发增删破坏。
3. **单个连接发送失败不能影响全桌**：逐条 ``try/except``，失败的连接记日志并从表里
   摘除（客户端 TCP 已经断了但服务端还没收到 close 帧的“僵尸连接”就是靠这里清掉）。
4. **慢客户端 = 背压问题**：本实现每条消息逐连接 ``await send``，最慢的客户端会拖慢
   同一轮广播。demo 规模（一桌 4 人）可以接受；生产要给每个连接加有界发送队列
   （满了就丢状态推送 / 断开），或用 ``asyncio.gather`` + 超时并发发送。
5. **只读查询用同步方法**：``connection_count`` / ``seats_online`` 只是读快照，
   在单事件循环模型下不需要抢锁；测试线程直接读也只看得到完整列表（CPython 下
   ``len`` / 集合推导不会被写操作撕裂），避免为了读一个计数再开协程。
"""

from __future__ import annotations

import asyncio
import json
import logging
from dataclasses import dataclass
from typing import Any

from starlette.websockets import WebSocket

from app.services.repository.base import SEATS

logger = logging.getLogger(__name__)

__all__ = ["Connection", "ConnectionManager"]


@dataclass(eq=False)
class Connection:
    """一条已建立的 WebSocket 连接。

    ``eq=False`` 让比较退化为「对象身份比较」：同一个 WebSocket 对象才是同一条连接，
    这样断线清理时可以安全地用 ``is`` / ``id`` 精确摘除，不会因为两个座位同名而误删。
    """

    seat: str
    """该连接绑定的座位（east / south / west / north）。"""

    websocket: WebSocket
    """Starlette 的 WebSocket 对象，负责实际的 send_text / close。"""


class ConnectionManager:
    """按 ``game_id`` 管理在线连接，负责广播与在线座位统计。

    一个应用实例（``create_app``）对应一个 ``ConnectionManager``，
    由 WebSocket 路由在首次连接时惰性挂到 ``app.state.ws_manager``，
    这样测试里的多个应用实例互不影响（不污染全局状态）。
    """

    def __init__(self) -> None:
        # 对局 id → 该局所有连接；同一个座位允许重复连接（多标签页 / 断线重连窗口）
        self._connections: dict[str, list[Connection]] = {}
        self._lock = asyncio.Lock()

    async def connect(self, game_id: str, seat: str, websocket: WebSocket) -> None:
        """登记一条新连接。

        :param game_id: 对局 id，广播按它隔离。
        :param seat: 连接绑定的座位。
        :param websocket: 已完成 accept 的 WebSocket 对象。
        """
        connection = Connection(seat=seat, websocket=websocket)
        async with self._lock:
            self._connections.setdefault(game_id, []).append(connection)
        logger.info(
            "WS 连接加入 game_id=%s seat=%s 当前连接数=%d",
            game_id,
            seat,
            self.connection_count(game_id),
        )

    async def disconnect(self, game_id: str, websocket: WebSocket) -> None:
        """摘除一条连接（幂等：不存在时直接返回）。

        只删除 ``websocket is`` 同一条连接，不会误删同座位的其它标签页连接。
        """
        async with self._lock:
            connections = self._connections.get(game_id)
            if not connections:
                return
            remaining = [item for item in connections if item.websocket is not websocket]
            if remaining:
                self._connections[game_id] = remaining
            else:
                self._connections.pop(game_id, None)
        logger.info(
            "WS 连接离开 game_id=%s 当前连接数=%d", game_id, self.connection_count(game_id)
        )

    async def broadcast(self, game_id: str, payload: dict[str, Any]) -> int:
        """把 ``payload`` 以 JSON 文本发给该局**所有**在线连接。

        单个连接发送失败（客户端已断开、网络抖动）只记日志并摘除该连接，
        不影响同局其他玩家收到消息。

        :param game_id: 对局 id。
        :param payload: 可 JSON 序列化的消息体。
        :return: 成功发送的连接数。
        """
        message = json.dumps(payload, ensure_ascii=False)
        # 先在锁内拍快照：后续逐条 await 发送时不持有锁，避免慢客户端拖住连接表
        async with self._lock:
            targets = list(self._connections.get(game_id, ()))

        sent = 0
        failed: list[Connection] = []
        for connection in targets:
            try:
                await connection.websocket.send_text(message)
                sent += 1
            except Exception as exc:  # noqa: BLE001 - 单连接故障不能扩散到全桌
                logger.info(
                    "WS 广播发送失败（将摘除该连接）game_id=%s seat=%s err=%r",
                    game_id,
                    connection.seat,
                    exc,
                )
                failed.append(connection)

        if failed:
            await self._remove_connections(game_id, failed)
        return sent

    async def _remove_connections(self, game_id: str, failed: list[Connection]) -> None:
        """从连接表里摘除发送失败的连接；调用前不应持有锁。"""
        failed_ids = {id(connection) for connection in failed}
        async with self._lock:
            connections = self._connections.get(game_id)
            if not connections:
                return
            remaining = [item for item in connections if id(item) not in failed_ids]
            if remaining:
                self._connections[game_id] = remaining
            else:
                self._connections.pop(game_id, None)
        logger.info("WS 已摘除 %d 条失效连接 game_id=%s", len(failed), game_id)

    def connection_count(self, game_id: str) -> int:
        """该局当前在线连接数（只读快照，加锁由写操作负责）。"""
        return len(self._connections.get(game_id, ()))

    def seats_online(self, game_id: str) -> list[str]:
        """该局在线座位列表，按东 → 南 → 西 → 北顺序返回（去重）。"""
        online = {connection.seat for connection in self._connections.get(game_id, ())}
        return [seat for seat in SEATS if seat in online]
