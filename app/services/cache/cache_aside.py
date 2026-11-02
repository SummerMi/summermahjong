"""cache-aside 读缓存层：给任意 :class:`GameRepository` 套一层 Redis 缓存。

定位：仓库里已有「Redis 当存储」（:class:`~app.services.repository.redis.RedisGameRepository`），
这里补的是**「Redis 当缓存」**——两者是不同角色：

- 存储：对局状态的唯一真相（内存 / SQL / Redis），重启后数据仍在；
- 缓存：只是读路径的加速层，丢了可以从源重新加载，**永远不是真相**。

用法（装饰器模式，对路由 / 调用方完全透明）::

    source = SqlGameRepository(engine, ...)         # 唯一真相
    cached = CachedGameRepository(source, redis_client, ttl_seconds=30)
    # 之后把 cached 当普通 GameRepository 用

读写路径（cache-aside 的标准姿势）：

1. **读** ``get``：``GET cache:game:{id}`` 命中 → 直接反序列化返回；
   未命中 → ``source.get()`` 回源 → ``SET ... EX ttl`` 写回缓存。
   源抛 ``GameNotFoundError`` 时**不缓存空值**（防穿透）。
2. **写** ``discard``：**先** ``source.discard()``（写库），**再** ``DEL`` 缓存。
   顺序不能反：先删缓存后写库，中间会有一整段窗口让并发读把**旧值**重新写回缓存，
   造成「缓存比库还旧」的脏数据；写后删最多让缓存空一段时间，下一次读回源即可。
3. **建** ``create``：写源后顺手 **write-through** 预写缓存。理由：客户端拿到
   ``game_id`` 后几乎必然立刻 ``GET`` 一次（拿完整手牌），预写把「建局后必有一次 miss」
   消掉；而且缓存内容就是刚提交的状态，不存在不一致风险。写失败只记 warning，不影响建局。
4. **清** ``clear``：清源 + 按 ``cache:game:*`` 前缀 ``SCAN`` 批量删，**不用** ``KEYS *``
   或 ``FLUSHDB``（会误伤同实例的其他业务数据）。

可用性：**缓存故障必须降级**。Redis 连接失败 / 超时 / 脏数据时只打 warning、记
``errors`` 计数，然后直接走 ``source``——缓存是加速层，不能因为它挂了让用户 500。
唯一的代价是降级期间读延迟回到源库水平。

可观测性：``hits`` / ``misses`` / ``errors`` 三个计数器 + :attr:`hit_rate` 命中率，
demo 时可以直接在日志 / 面板里展示「缓存到底有没有用」。

key 隔离：前缀 ``cache:game:`` 与 Redis 存储实现的 ``game:{id}`` 刻意分开——
生产里两者可能共用同一个 Redis 实例，混用前缀会让缓存和存储互相覆盖。

序列化：缓存值是**一条扁平 JSON 文档**（而不是复用 ``repository/redis.py`` 里
Hash 字段编码）。原因：缓存是 ``GET/SET`` 的单值，单条 JSON 用 ``redis-cli GET``
就能直接看、格式与 Hash 存储解耦；复用 Hash 编码会把「字段是 JSON 字符串」再套一层
JSON，既难读也把两种存储格式绑死。字段与 :class:`GameState` 一一对应，读回时校验
``game_id`` 与 key 一致，防止 key 冲突导致的串局。

测试：客户端由调用方注入（生产 ``redis.asyncio.Redis``，测试
``fakeredis.aioredis.FakeRedis``），本模块只依赖 duck typing 的
:class:`AsyncCacheClient`，不 import 具体客户端实现。
"""

from __future__ import annotations

import json
import logging
from collections.abc import AsyncIterator
from datetime import UTC, datetime
from typing import Any, Protocol

from app.services.repository.base import SEATS, GameRepository, GameState

logger = logging.getLogger(__name__)

CACHE_KEY_PREFIX = "cache:game:"
"""缓存 key 前缀；与 Redis 存储实现的 ``game:`` 前缀刻意隔离，避免互相覆盖。"""


class AsyncCacheClient(Protocol):
    """缓存客户端的最小接口（``redis.asyncio.Redis`` / ``fakeredis`` 都满足）。

    这里用 :class:`typing.Protocol` 描述 duck typing，而不是 import
    ``redis.asyncio.Redis``：缓存层不绑定具体客户端，测试里注入 fakeredis 或
    「每次操作都抛异常」的假客户端都不需要真 Redis，也不会给内存模式引入可选依赖。
    """

    async def get(self, name: str) -> Any:
        """读取一个缓存值；不存在返回 ``None``。"""
        ...

    async def set(self, name: str, value: str, ex: int | None = None) -> Any:
        """写入一个缓存值，``ex`` 为过期秒数。"""
        ...

    async def delete(self, *names: str) -> Any:
        """删除若干缓存 key。"""
        ...

    def scan_iter(self, match: str | None = None) -> AsyncIterator[Any]:
        """按 glob 前缀增量遍历 key（生产实现是 ``SCAN``，不是 ``KEYS``）。"""
        ...


def _state_to_json(state: GameState) -> str:
    """把对局快照编码成单条 JSON 文档（缓存值）。

    ``created_at`` 用 ISO 8601 带时区字符串，读回时能还原成带 UTC 的 ``datetime``；
    其余字段与 :class:`GameState` 同名，方便排查问题时直接看缓存内容。
    """
    return json.dumps(
        {
            "game_id": state.game_id,
            "seed": state.seed,
            "wall": state.wall,
            "hands": state.hands,
            "discards": state.discards,
            "current_player": state.current_player,
            "created_at": state.created_at.isoformat(),
            "last_access": state.last_access,
            "version": state.version,
        },
        ensure_ascii=False,
        separators=(",", ":"),
    )


def _state_from_json(raw: str, *, expected_game_id: str) -> GameState:
    """把缓存里的 JSON 文档解码回 :class:`GameState`，并做最小完整性校验。

    :param raw: 缓存值（``decode_responses=True`` 下是 ``str``）。
    :param expected_game_id: 本次查询的 ``game_id``；缓存内容必须与之一致，
        否则视为脏缓存（防止 key 冲突 / 手工改坏导致串局）。
    :raises ValueError: 不是合法 JSON / 缺字段 / id 不匹配。
    :raises TypeError: JSON 结构正确但字段类型不对（例如 wall 不是数组）。
    """
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError(f"缓存值不是合法 JSON：{exc}") from exc
    if not isinstance(data, dict):
        raise TypeError("缓存值必须是 JSON 对象")
    if data.get("game_id") != expected_game_id:
        raise ValueError("缓存内容的 game_id 与 key 不匹配")

    required = ("seed", "wall", "hands", "discards", "current_player", "created_at",
                "last_access", "version")
    missing = [field for field in required if field not in data]
    if missing:
        raise ValueError(f"缓存快照缺少字段：{missing}")
    if not isinstance(data["wall"], list):
        raise TypeError("缓存字段 wall 必须是数组")
    if not isinstance(data["hands"], dict) or not isinstance(data["discards"], dict):
        raise TypeError("缓存字段 hands / discards 必须是对象")
    if data["current_player"] not in SEATS:
        raise ValueError(f"缓存字段 current_player 非法：{data['current_player']!r}")

    created_at = datetime.fromisoformat(data["created_at"])
    if created_at.tzinfo is None:  # 缓存跨时区 / 手工写入时统一补 UTC
        created_at = created_at.replace(tzinfo=UTC)
    return GameState(
        game_id=expected_game_id,
        seed=data["seed"],
        wall=list(data["wall"]),
        hands={seat: list(tiles) for seat, tiles in data["hands"].items()},
        discards={seat: list(tiles) for seat, tiles in data["discards"].items()},
        current_player=data["current_player"],
        created_at=created_at,
        last_access=float(data["last_access"]),
        version=int(data["version"]),
    )


class CachedGameRepository(GameRepository):
    """对 ``source`` 的 cache-aside 装饰器，本身也是 :class:`GameRepository`。

    ``source`` 是唯一真相，Redis 只负责「读加速」。所有方法在缓存不可用时都能
    自动降级到 ``source``，因此可以把缓存当成「可选的性能优化」而不是依赖。
    """

    def __init__(
        self,
        source: GameRepository,
        client: AsyncCacheClient,
        ttl_seconds: int = 30,
        *,
        key_prefix: str = CACHE_KEY_PREFIX,
    ) -> None:
        """
        :param source: 被装饰的对局仓库（内存 / SQL / Redis 均可），缓存永远以它为准。
        :param client: 缓存客户端（``redis.asyncio.Redis`` / fakeredis），只用到
            ``get`` / ``set`` / ``delete`` / ``scan_iter`` 四个异步方法。
        :param ttl_seconds: 缓存 TTL（秒）；``0`` 表示关闭缓存，所有操作直通 ``source``。
        :param key_prefix: 缓存 key 前缀，默认 ``cache:game:``。
        """
        if ttl_seconds < 0:
            raise ValueError("ttl_seconds 必须 >= 0（0 表示关闭缓存）")
        self._source = source
        self._client = client
        self._ttl_seconds = ttl_seconds
        self._key_prefix = key_prefix
        self._hits = 0
        self._misses = 0
        self._errors = 0

    @property
    def source(self) -> GameRepository:
        """被装饰的源仓库（唯一真相）。"""
        return self._source

    @property
    def ttl_seconds(self) -> int:
        """缓存 TTL（秒）；``0`` 表示缓存关闭。"""
        return self._ttl_seconds

    @property
    def enabled(self) -> bool:
        """缓存是否启用（``ttl_seconds=0`` 时关闭，全部直通 source）。"""
        return self._ttl_seconds > 0

    @property
    def hits(self) -> int:
        """缓存命中次数。"""
        return self._hits

    @property
    def misses(self) -> int:
        """缓存未命中次数（含缓存故障降级）。"""
        return self._misses

    @property
    def errors(self) -> int:
        """缓存操作失败次数（连接失败 / 超时 / 脏数据），用于观测降级频率。"""
        return self._errors

    @property
    def hit_rate(self) -> float:
        """命中率 = hits / (hits + misses)；还没有读请求时返回 ``0.0``。"""
        total = self._hits + self._misses
        return self._hits / total if total else 0.0

    def cache_key(self, game_id: str) -> str:
        """返回一局对局的缓存 key。"""
        return f"{self._key_prefix}{game_id}"

    async def create(self, seed: int | None = None) -> GameState:
        """建局：先写源，再 write-through 预写缓存（失败不影响建局）。"""
        state = await self._source.create(seed=seed)
        if self.enabled:
            await self._write_cache(state)
        return state

    async def get(self, game_id: str) -> GameState:
        """读对局：缓存命中直接返回；未命中回源并写回缓存。

        :raises GameNotFoundError: 源里没有这一局；**空结果不写缓存**（防穿透）。
        """
        if not self.enabled:
            return await self._source.get(game_id)

        cached = await self._read_cache(game_id)
        if cached is not None:
            self._hits += 1
            return cached

        self._misses += 1
        state = await self._source.get(game_id)  # 不存在时在这里抛 GameNotFoundError
        await self._write_cache(state)
        return state

    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        """打牌：先写源（提交成功），再删缓存（写后删，cache-aside 的标准姿势）。

        顺序颠倒会引入「先删缓存 → 并发读回填旧值 → 写库」的脏缓存窗口；
        本实现只允许「缓存比源库旧最多一个回源窗口」，且写后删让最长不一致窗口
        收敛为「写提交到 DEL 完成」之间的毫秒级。
        """
        state = await self._source.discard(game_id, seat, tile)
        if self.enabled:
            await self._delete_cache(game_id)
        return state

    async def clear(self) -> None:
        """清空源 + 按前缀清空缓存（测试 / 运维用）。"""
        await self._source.clear()
        if self.enabled:
            await self._clear_cache()

    async def _read_cache(self, game_id: str) -> GameState | None:
        """读缓存；任何异常都返回 ``None``（降级回源），并记 ``errors``。"""
        try:
            raw = await self._client.get(self.cache_key(game_id))
        except Exception as exc:  # noqa: BLE001 - 缓存故障一律降级，不能影响主流程
            self._errors += 1
            logger.warning("读缓存失败，降级回源 game_id=%s error=%s", game_id, exc)
            return None
        if raw is None:
            return None
        if isinstance(raw, bytes):  # 客户端没开 decode_responses 时兼容一下
            raw = raw.decode("utf-8")
        try:
            return _state_from_json(raw, expected_game_id=game_id)
        except (TypeError, ValueError) as exc:
            # 脏缓存（旧格式 / 手工改坏 / key 冲突）：删掉按未命中处理，不让坏数据进业务
            self._errors += 1
            logger.warning("缓存内容无法解析，已删除 game_id=%s error=%s", game_id, exc)
            await self._delete_cache(game_id)
            return None

    async def _write_cache(self, state: GameState) -> None:
        """写缓存（``SET ... EX ttl``）；失败只记 warning / ``errors``。"""
        try:
            await self._client.set(
                self.cache_key(state.game_id),
                _state_to_json(state),
                ex=self._ttl_seconds,
            )
        except Exception as exc:  # noqa: BLE001 - 缓存写失败不影响读路径正确性
            self._errors += 1
            logger.warning("写缓存失败（忽略，之后读会回源） game_id=%s error=%s", state.game_id, exc)

    async def _delete_cache(self, game_id: str) -> None:
        """删缓存；失败只记 warning / ``errors``（缓存会在 TTL 后自然过期）。"""
        try:
            await self._client.delete(self.cache_key(game_id))
        except Exception as exc:  # noqa: BLE001 - 删失败不能把打牌请求变成 500
            self._errors += 1
            logger.warning("删缓存失败（依赖 TTL 兜底） game_id=%s error=%s", game_id, exc)

    async def _clear_cache(self) -> None:
        """按前缀 ``SCAN`` 批量删缓存；失败只记 warning / ``errors``。"""
        deleted = 0
        batch: list[str] = []
        try:
            async for key in self._client.scan_iter(match=f"{self._key_prefix}*"):
                batch.append(key.decode("utf-8") if isinstance(key, bytes) else key)
                if len(batch) >= 100:
                    deleted += int(await self._client.delete(*batch))
                    batch.clear()
            if batch:
                deleted += int(await self._client.delete(*batch))
        except Exception as exc:  # noqa: BLE001 - 缓存清理失败不影响源已清空
            self._errors += 1
            logger.warning("清缓存失败（残留 key 会在 TTL 后过期） error=%s", exc)
            return
        if deleted:
            logger.info("清空对局缓存 %d 个 key", deleted)
