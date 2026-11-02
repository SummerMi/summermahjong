"""Redis 对局仓库：Hash 快照 + ``WATCH``/``MULTI`` 乐观锁 + ZSET 做 LRU/TTL + ``EXPIRE``。

这是 :class:`~app.services.repository.base.GameRepository` 的第三个实现，
和内存 ``GameStore``、SQL ``SqlGameRepository`` 并列，路由层依然一行都不用改
（依赖倒置的红利，见 ``docs/adr-0003-redis存储与缓存.md``）。

存储模型（面试可直接画）::

    HSET game:{game_id} wall=<json> hands=<json> discards=<json> \
                        current_player=east seed=42 created_at=... last_access=... version=0
    EXPIRE game:{game_id} 7200                          # 闲置 TTL
    ZADD games:index {last_access} {game_id}            # LRU 上限 + TTL 的索引

为什么是 Hash 而不是拆成 100 多个 ``tile:*`` 键：

- 对局是**聚合根**：一次请求要整份快照（牌墙 83 张 + 四家手牌 + 四家牌河），
  ``HGETALL`` 一次网络往返即可；拆成细粒度 key 会退化成多次 ``GET`` + 事务范围膨胀；
- 但也不把四个座位拆成四个 Hash：座位手牌总是一起读、一起写，拆开只会让
  ``WATCH`` 要盯四个 key，冲突概率与代码复杂度都上升。

为什么 ``wall``/``hands``/``discards`` 用 JSON 字符串塞进 field：

- 结构是嵌套的（``hands`` 是 ``dict[seat, list[tile]]``），Redis Hash 的 field 只能是
  字符串，JSON 是这里最直接的序列化；
- 好处是「一种存储一份快照」——和 SQL 实现的 JSON 列、内存实现的 dataclass 完全同构，
  三种实现共享 ``base.py`` 里的纯函数，同 seed 同牌局。

并发控制（打牌原子性）：

- ``WATCH game:{id}`` → ``HGETALL`` 读快照 → 纯函数 :func:`apply_discard` 算出新快照
  → ``MULTI`` + ``HSET`` + ``EXPIRE`` + ``ZADD`` + ``EXEC``；
- 读和写之间只要有人改过这个 key，``EXEC`` 就返回 ``WatchError``（CAS 失败），
  抛 :class:`~app.services.repository.base.ConcurrentUpdateError`
  （统一信封映射为 409 ``GAME_CONFLICT``），**绝不静默覆盖**；
- 不做自动重试：打牌不是幂等操作，冲突后由客户端重试能拿到最新状态再决策；
  服务端无脑重试会在同一局上形成「重试风暴」。重试策略见 ADR。

上限与淘汰：``games:index`` 是 ZSET，score = 最近访问的 Unix 时间戳；
- TTL：``EXPIRE`` 管真实过期，索引 score 供 ``ZRANGEBYSCORE`` 批量清理过期对局；
- LRU：创建新对局前 ``ZCARD`` 判断是否到 ``max_games``，超了用 ``ZPOPMIN`` 弹掉最旧的。

时钟：这里用 ``time.time()``（和 SQL 实现一致，跨进程 / 跨重启可比较），
内存实现的 ``time.monotonic()`` 只在单进程内比较，见 ``GameState.last_access`` 的注释。

测试：本机没有真 Redis 服务，单测用 ``fakeredis.aioredis.FakeRedis`` 注入同一个
``redis.asyncio.Redis`` 接口；生产只需要把 ``REDIS_URL`` 指向真实例，代码不用改。
"""

from __future__ import annotations

import json
import logging
import time
from collections.abc import Mapping
from datetime import UTC, datetime
from typing import Any

from redis.asyncio import Redis
from redis.asyncio.client import Pipeline
from redis.exceptions import WatchError

from app.core.errors import GameNotFoundError
from app.services.repository.base import (
    ConcurrentUpdateError,
    GameRepository,
    GameState,
    apply_discard,
    new_game_state,
)

logger = logging.getLogger(__name__)

KEY_PREFIX = "game:"
"""对局 Hash 的 key 前缀；完整 key 为 ``game:{game_id}``。"""

INDEX_KEY = "games:index"
"""LRU / TTL 索引：ZSET，member = ``game_id``，score = 最近访问的 Unix 时间戳（秒）。"""


def game_key(game_id: str) -> str:
    """返回一局对局在 Redis 里的 Hash key。"""
    return f"{KEY_PREFIX}{game_id}"


def create_redis_client(redis_url: str) -> Redis:
    """按 ``REDIS_URL`` 创建异步客户端（单例，交给 lifespan 管理生命周期）。

    ``decode_responses=True``：本模块的字段值都是文本（JSON / 数字字符串），
    统一解码成 ``str``，避免业务代码里到处 ``.decode()``。

    :param redis_url: 例如 ``redis://127.0.0.1:6379/0`` 或
        ``redis://:password@redis:6379/1``。
    :return: 尚未真正建连的 ``redis.asyncio.Redis``（命令首次执行时才取连接）。
    """
    return Redis.from_url(redis_url, decode_responses=True)


def _encode_state(state: GameState) -> dict[str, str]:
    """把领域快照编码成 Redis Hash 的 field → 值（全部为字符串）。

    嵌套结构走 JSON；``seed`` 用 ``json.dumps`` 是为了无损表达 ``None``。
    """
    return {
        "wall": json.dumps(state.wall, ensure_ascii=False),
        "hands": json.dumps(state.hands, ensure_ascii=False),
        "discards": json.dumps(state.discards, ensure_ascii=False),
        "current_player": state.current_player,
        "seed": json.dumps(state.seed),
        "created_at": state.created_at.isoformat(),
        "last_access": repr(state.last_access),
        "version": str(state.version),
    }


def _decode_state(raw: Mapping[str, Any], game_id: str) -> GameState:
    """把 ``HGETALL`` 的结果解码回领域快照。

    ``wall`` / ``hands`` / ``discards`` 做拷贝，保证后续 :func:`apply_discard`
    就地修改时不会改到解析出来的容器（与 SQL 实现的 ``_row_to_state`` 同理）。

    :param raw: ``HGETALL`` 的映射；调用方需保证 key 存在且字段完整。
    :param game_id: 对局 id（Hash 里不重复存，key 本身就是 id）。
    :return: 完整对局快照。
    """
    created_at = datetime.fromisoformat(raw["created_at"])
    if created_at.tzinfo is None:  # 兼容手工写入 / 未来可能出现的无时区数据
        created_at = created_at.replace(tzinfo=UTC)
    return GameState(
        game_id=game_id,
        seed=json.loads(raw["seed"]),
        wall=list(json.loads(raw["wall"])),
        hands={seat: list(tiles) for seat, tiles in json.loads(raw["hands"]).items()},
        discards={seat: list(tiles) for seat, tiles in json.loads(raw["discards"]).items()},
        current_player=raw["current_player"],
        created_at=created_at,
        last_access=float(raw["last_access"]),
        version=int(raw["version"]),
    )


class RedisGameRepository(GameRepository):
    """基于 Redis 的对局仓库。

    四个方法都只做「存取 + 并发保护」，发牌 / 打牌的规则全部复用
    :mod:`app.services.repository.base` 里的纯函数，因此三种实现的行为（含错误码）
    天然一致。实例不持有连接池，``client`` 由调用方（lifespan）创建与关闭。
    """

    def __init__(self, client: Redis, *, max_games: int = 100, ttl_seconds: int = 7200) -> None:
        """
        :param client: ``redis.asyncio.Redis``（生产）或 ``fakeredis`` 的同类客户端（测试）。
        :param max_games: 同时保留的对局数上限，超过时淘汰最久未访问的一局。
        :param ttl_seconds: 闲置多久后过期（同时用作 Redis ``EXPIRE`` 秒数）。
        """
        if max_games < 1:
            raise ValueError("max_games 必须 >= 1")
        if ttl_seconds < 1:
            raise ValueError("ttl_seconds 必须 >= 1")
        self._client = client
        self._max_games = max_games
        self._ttl_seconds = ttl_seconds

    @property
    def client(self) -> Redis:
        """底层异步客户端（lifespan 关闭时 ``aclose()`` 用）。"""
        return self._client

    @property
    def max_games(self) -> int:
        """对局数量上限。"""
        return self._max_games

    @property
    def ttl_seconds(self) -> int:
        """对局闲置过期阈值（秒），与 ``EXPIRE`` 的秒数一致。"""
        return self._ttl_seconds

    async def create(self, seed: int | None = None) -> GameState:
        """发牌并写入一局新对局（Hash + EXPIRE + 索引），返回初始快照。

        写入前先做 TTL 清理与上限淘汰，保证 Redis 里的对局数不会无限增长。
        一次 ``MULTI/EXEC`` 完成「Hash + 过期时间 + 索引」三件事，
        不会出现「对局写了但没进索引」导致 LRU 失真的中间态。

        :param seed: 随机种子；相同种子在三种实现下发出同一副牌。
        :return: 发牌完成的快照（``east`` 14 张、其余 13 张，牌墙 83 张）。
        """
        await self._purge_expired()
        evicted = await self._evict_overflow()

        state = new_game_state(seed)
        now = time.time()
        state.last_access = now
        key = game_key(state.game_id)

        async with self._client.pipeline(transaction=True) as pipe:
            pipe.hset(key, mapping=_encode_state(state))
            pipe.expire(key, self._ttl_seconds)
            pipe.zadd(INDEX_KEY, {state.game_id: now})
            await pipe.execute()

        logger.info(
            "创建对局（Redis） game_id=%s seed=%s wall_remaining=%d evicted=%s",
            state.game_id,
            seed,
            len(state.wall),
            evicted,
        )
        return state

    async def get(self, game_id: str) -> GameState:
        """按 id 读取对局快照，并刷新其 TTL 与 LRU 位置。

        ``EXPIRE`` + ``ZADD`` 都是幂等操作，不 ``WATCH``：读操作即使与打牌并发，
        也只会读到「打牌前」或「打牌后」的完整快照（Hash 的整份写入是原子的），
        不需要为一次「摸一下」的读去争乐观锁。

        :raises GameNotFoundError: 对局不存在或已过期 / 被 LRU 淘汰。
        """
        await self._purge_expired()
        key = game_key(game_id)
        raw = await self._client.hgetall(key)
        if not raw:
            raise GameNotFoundError(game_id)

        state = _decode_state(raw, game_id)
        now = time.time()
        state.last_access = now
        async with self._client.pipeline(transaction=True) as pipe:
            # 只续期 / 挪 LRU 位置，不重写 Hash：万一 key 刚好过期，EXPIRE 返回 0，
            # 不会把一份半残的快照「复活」出来。
            pipe.expire(key, self._ttl_seconds)
            pipe.zadd(INDEX_KEY, {game_id: now})
            await pipe.execute()
        return state

    async def discard(self, game_id: str, seat: str, tile: str) -> GameState:
        """用 ``WATCH`` + ``MULTI/EXEC`` 原子地执行一次打牌（乐观锁 CAS）。

        流程：``WATCH`` → 读快照 → 纯函数校验并迁移状态 → ``MULTI`` 后整份写回。
        只要读与写之间有别人提交过这局（打牌 / 任何写），``EXEC`` 就会失败，
        这里抛 :class:`ConcurrentUpdateError`（409 ``GAME_CONFLICT``）让客户端重试；
        校验失败（非法牌 / 不在手 / 非当前回合）时不会写回任何字段。

        :raises GameNotFoundError: 对局不存在。
        :raises InvalidHandError: ``tile`` 不是合法牌面（与内存 / SQL 实现同码）。
        :raises TileNotInHandError: ``tile`` 不在该座位手牌里。
        :raises AppError: 还没轮到该座位出牌（``NOT_YOUR_TURN``）。
        :raises ConcurrentUpdateError: 乐观锁冲突（409）。
        """
        await self._purge_expired()
        key = game_key(game_id)

        async with self._client.pipeline(transaction=True) as pipe:
            await pipe.watch(key)  # WATCH 必须在读之前
            raw = await self._read_watched(pipe, key)
            if not raw:
                await pipe.unwatch()
                raise GameNotFoundError(game_id)

            state = _decode_state(raw, game_id)
            next_player = apply_discard(state, seat, tile)  # 纯函数：校验 + 状态迁移
            now = time.time()
            state.version += 1
            state.last_access = now

            pipe.multi()
            pipe.hset(key, mapping=_encode_state(state))
            pipe.expire(key, self._ttl_seconds)
            pipe.zadd(INDEX_KEY, {game_id: now})
            try:
                await pipe.execute()
            except WatchError as exc:
                raise ConcurrentUpdateError(game_id) from exc

        logger.info(
            "打牌（Redis） game_id=%s seat=%s tile=%s next=%s wall_remaining=%d version=%d",
            game_id,
            seat,
            tile,
            next_player,
            len(state.wall),
            state.version,
        )
        return state

    async def clear(self) -> None:
        """删除本仓库写入的所有对局与索引（测试 / 运维用）。

        用 ``SCAN`` 按前缀删，**不用** ``FLUSHDB``：生产 Redis 往往和缓存、限流等
        其他业务共用实例，``FLUSHDB`` 会连累别人；前缀扫描虽然遍历全库，但本方法
        只在测试 / 手动运维时调用，代价可接受。
        """
        deleted = 0
        batch: list[str] = []
        async for key in self._client.scan_iter(match=f"{KEY_PREFIX}*"):
            batch.append(key)
            if len(batch) >= 100:
                deleted += int(await self._client.delete(*batch))
                batch.clear()
        if batch:
            deleted += int(await self._client.delete(*batch))
        await self._client.delete(INDEX_KEY)
        if deleted:
            logger.info("清空 Redis 对局 %d 局", deleted)

    async def _read_watched(self, pipe: Pipeline, key: str) -> dict[str, Any]:
        """``WATCH`` 之后、``MULTI`` 之前的读（独立成方法便于测试放大并发窗口）。"""
        return await pipe.hgetall(key)

    async def _purge_expired(self) -> int:
        """批量清理已过期对局：``ZRANGEBYSCORE`` 找过期的 score，再删 Hash 与索引项。

        用 ``WATCH`` 盯住索引：如果并发写者刚刷新过自己的 score，本次清理整体放弃、
        下一次操作再试，避免「刚被访问的对局被误删」。
        """
        threshold = time.time() - self._ttl_seconds
        async with self._client.pipeline(transaction=True) as pipe:
            try:
                await pipe.watch(INDEX_KEY)
                stale_ids = await pipe.zrangebyscore(INDEX_KEY, "-inf", threshold)
                if not stale_ids:
                    await pipe.unwatch()
                    return 0
                pipe.multi()
                pipe.zrem(INDEX_KEY, *stale_ids)
                pipe.delete(*(game_key(game_id) for game_id in stale_ids))
                await pipe.execute()
            except WatchError:  # pragma: no cover - 并发刷新索引时的保守退让
                return 0
        logger.info("TTL 清理对局（Redis） %d 局", len(stale_ids))
        return len(stale_ids)

    async def _evict_overflow(self) -> list[str]:
        """对局数达到上限时淘汰最久未访问的若干局，返回被淘汰的 id 列表。

        ``ZPOPMIN`` 在 ``MULTI`` 里弹出 score 最小的成员（score = 最近访问时间，
        所以最小即最久未访问）；弹出后再删对应 Hash。两步之间如果进程崩溃，
        残留的 Hash 会由 ``EXPIRE`` 兜底回收。
        """
        async with self._client.pipeline(transaction=True) as pipe:
            try:
                await pipe.watch(INDEX_KEY)
                total = await pipe.zcard(INDEX_KEY)
                if total < self._max_games:
                    await pipe.unwatch()
                    return []
                excess = total - self._max_games + 1  # +1 给即将创建的新对局腾位置
                pipe.multi()
                pipe.zpopmin(INDEX_KEY, excess)
                results = await pipe.execute()
            except WatchError:  # pragma: no cover - 并发创建时的保守退让
                return []

        popped = results[-1]
        evicted = [str(member) for member, _score in popped]
        if evicted:
            await self._client.delete(*(game_key(game_id) for game_id in evicted))
            logger.warning("对局数达到上限 %d，淘汰最久未访问的 %d 局", self._max_games, len(evicted))
        return evicted
