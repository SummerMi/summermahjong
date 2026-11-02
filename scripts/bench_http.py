"""HTTP 压测脚本：给「本机 loopback、单进程 uvicorn、Python 客户端」的实测用。

用法（先另开一个终端起服务）：

    python -m uvicorn app.main:app --port 8000
    python scripts/bench_http.py --all
    python scripts/bench_http.py --scenario tenpai --duration 10 --concurrency 20

输出：每个场景的 请求数 / RPS / p50 / p90 / p99 / 错误数。

⚠️ 口径声明（写简历 / 面试时必须带上）：
- 这是**本机回环**压测，客户端和服务端在同一台机器、抢同一个 CPU；
- 客户端是 Python（GIL 单线程异步），在高并发下会先成为瓶颈；
- 因此这些数字**不是生产 QPS**，只能用来做「改前 / 改后」的相对比较；
- 要报生产级数字请用 wrk / hey 在独立压测机上打。
"""

from __future__ import annotations

import argparse
import asyncio
import statistics
import time

import httpx

# 每个场景：(HTTP 方法, 路径, 请求体)
SCENARIOS: dict[str, tuple[str, str, dict | None]] = {
    "healthz": ("GET", "/healthz", None),
    "win-check": (
        "POST",
        "/api/v1/mahjong/win-check",
        {"hand": ["1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "1p", "E", "E"]},
    ),
    "tenpai": (
        "POST",
        "/api/v1/mahjong/tenpai",
        {"hand": ["1m", "2m", "3m", "4m", "5m", "6m", "7m", "8m", "9m", "1p", "1p", "1p", "2s"]},
    ),
    "games-create": ("POST", "/api/v1/games", {"seed": 42}),
}


def _percentile(sorted_values: list[float], pct: float) -> float:
    """取分位数（输入必须已排序）；空列表返回 0。"""
    if not sorted_values:
        return 0.0
    index = min(len(sorted_values) - 1, round(pct / 100.0 * (len(sorted_values) - 1)))
    return sorted_values[index]


async def _worker(
    client: httpx.AsyncClient,
    method: str,
    path: str,
    payload: dict | None,
    stop_at: float,
    latencies: list[float],
    errors: list[str],
) -> None:
    """一个并发工作协程：一直打到截止时间为止，记录每次请求耗时。"""
    while time.perf_counter() < stop_at:
        started = time.perf_counter()
        try:
            if method == "GET":
                response = await client.get(path)
            else:
                response = await client.post(path, json=payload)
            if response.status_code >= 400:
                errors.append(f"HTTP {response.status_code}")
        except Exception as exc:  # noqa: BLE001 - 压测脚本要记录所有异常
            errors.append(type(exc).__name__)
        latencies.append((time.perf_counter() - started) * 1000.0)


async def run_scenario(
    base_url: str,
    name: str,
    duration: float,
    concurrency: int,
    warmup: int,
) -> dict:
    """跑一个场景并返回统计结果。"""
    method, path, payload = SCENARIOS[name]
    latencies: list[float] = []
    errors: list[str] = []

    limits = httpx.Limits(max_connections=concurrency * 2, max_keepalive_connections=concurrency * 2)
    # trust_env=False：压测打的是本机回环，不需要走系统代理；
    # 另外某些机器上 NO_PROXY 里写成 `[::1]` 会让 httpx 解析代理白名单时报
    # `InvalidURL: Invalid port: ':1]'`，关掉环境变量读取可以绕开这个坑。
    async with httpx.AsyncClient(
        base_url=base_url, timeout=10.0, limits=limits, trust_env=False
    ) as client:
        # 预热：让连接池、路由缓存、JIT 预热到位，避免把冷启动算进结果
        for _ in range(warmup):
            await (client.get(path) if method == "GET" else client.post(path, json=payload))

        started = time.perf_counter()
        stop_at = started + duration
        await asyncio.gather(
            *(
                _worker(client, method, path, payload, stop_at, latencies, errors)
                for _ in range(concurrency)
            )
        )
        elapsed = time.perf_counter() - started

    latencies.sort()
    return {
        "scenario": name,
        "requests": len(latencies),
        "rps": len(latencies) / elapsed if elapsed > 0 else 0.0,
        "p50": _percentile(latencies, 50),
        "p90": _percentile(latencies, 90),
        "p99": _percentile(latencies, 99),
        "mean": statistics.fmean(latencies) if latencies else 0.0,
        "errors": len(errors),
        "error_sample": errors[:3],
    }


async def main_async(args: argparse.Namespace) -> None:
    """按参数跑一个或全部场景，打印表格。"""
    names = list(SCENARIOS) if args.all else [args.scenario]
    print(
        f"\n目标 {args.base_url} ｜ 并发 {args.concurrency} ｜ 每场景 {args.duration}s ｜ "
        f"单机 loopback（数字非生产 QPS）\n"
    )
    header = f"{'场景':<14}{'请求数':>9}{'RPS':>10}{'p50(ms)':>10}{'p90(ms)':>10}{'p99(ms)':>10}{'错误':>7}"
    print(header)
    print("-" * len(header))
    for name in names:
        result = await run_scenario(args.base_url, name, args.duration, args.concurrency, args.warmup)
        print(
            f"{result['scenario']:<14}{result['requests']:>9}{result['rps']:>10.1f}"
            f"{result['p50']:>10.2f}{result['p90']:>10.2f}{result['p99']:>10.2f}{result['errors']:>7}"
        )
        if result["error_sample"]:
            print(f"  错误样例：{result['error_sample']}")
    print()


def parse_args() -> argparse.Namespace:
    """解析命令行参数。"""
    parser = argparse.ArgumentParser(description="本机 loopback HTTP 压测")
    parser.add_argument("--base-url", default="http://127.0.0.1:8000", help="服务地址")
    parser.add_argument("--scenario", default="tenpai", choices=list(SCENARIOS), help="场景名")
    parser.add_argument("--all", action="store_true", help="跑全部场景")
    parser.add_argument("--duration", type=float, default=10.0, help="每个场景持续秒数")
    parser.add_argument("--concurrency", type=int, default=20, help="并发协程数")
    parser.add_argument("--warmup", type=int, default=20, help="预热请求数")
    return parser.parse_args()


if __name__ == "__main__":
    asyncio.run(main_async(parse_args()))
