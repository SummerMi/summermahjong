#!/usr/bin/env python
"""Python 侧性能对照：用 timeit 测与 Go benchmark 完全相同的输入用例。

用法（在仓库根目录执行）：

    .venv/Scripts/python.exe go-engine/tools/bench_python.py

输出：
- 控制台打印 Markdown 表格（可直接粘进 README）；
- ``go-engine/testdata/python_bench.json`` 保存原始数字，便于复核。

口径：
- 同机、单线程、非压测；
- 每个用例跑 timeit.repeat，取最小值（best-of），再除以调用次数得到单次耗时；
- 扣掉 ``lambda: None`` 的函数调用 / timeit 开销（几十 ns 量级），避免虚高；
- 输入用例来自 ``go-engine/testdata/bench_cases.json``，与 Go 侧共用。
"""

from __future__ import annotations

import json
import sys
import timeit
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent
ENGINE_DIR = TOOLS_DIR.parent
REPO_ROOT = ENGINE_DIR.parent
TESTDATA_DIR = ENGINE_DIR / "testdata"

sys.path.insert(0, str(REPO_ROOT))

from app.services.mahjong.rules import waiting_tiles, winning_pattern  # noqa: E402

NUMBERS = (500, 2000, 10000, 50000, 200000)


def 手牌(text: str) -> list[str]:
    """把 "123m 456p EE" 简写解析成牌面列表（与 tests / Go 侧规则一致）。"""
    tiles: list[str] = []
    for group in text.split():
        if group[-1] in {"m", "p", "s"}:
            tiles.extend(f"{rank}{group[-1]}" for rank in group[:-1])
        else:
            tiles.extend(group)
    return tiles


def measure(fn, arg) -> dict:
    """跑 timeit 并返回扣掉调用开销后的单次耗时（ns）。"""
    timer = timeit.Timer(lambda: fn(arg))
    # 粗校准：找一个让单轮耗时约 50ms 的调用次数。
    pilot = min(timer.repeat(repeat=3, number=200)) / 200
    number = max(100, min(200_000, int(0.05 / max(pilot, 1e-9))))

    raw = min(timer.repeat(repeat=7, number=number)) / number
    overhead = overhead_ns()
    adjusted = max(raw - overhead, 0.0)
    return {
        "number": number,
        "repeat": 7,
        "raw_ns_per_call": round(raw * 1e9, 2),
        "call_overhead_ns": round(overhead * 1e9, 2),
        "ns_per_call": round(adjusted * 1e9, 2),
        "us_per_call": round(adjusted * 1e6, 3),
    }


def overhead_ns() -> float:
    """用空 lambda 测 timeit + 调用开销。"""
    timer = timeit.Timer(lambda: None)
    return min(timer.repeat(repeat=7, number=100_000)) / 100_000


def main() -> None:
    payload = json.loads((TESTDATA_DIR / "bench_cases.json").read_text(encoding="utf-8"))

    results: dict[str, list[dict]] = {"win": [], "wait": []}
    for group, fn, expected_size in (
        ("win", winning_pattern, 14),
        ("wait", waiting_tiles, 13),
    ):
        print(f"\n## {group} ({expected_size} tiles)\n")
        print("| case | calls/round | ns/op (best) | us/op (best) |")
        print("| --- | ---: | ---: | ---: |")
        for case in payload[group]:
            hand = 手牌(case["hand"])
            assert len(hand) == expected_size, (case["id"], len(hand))
            stats = measure(fn, hand)
            results[group].append({"id": case["id"], "hand": case["hand"], **stats})
            print(f"| {case['id']} | {stats['number']} | {stats['ns_per_call']:.1f} | {stats['us_per_call']:.3f} |")

    meta = {
        "python_version": sys.version.split()[0],
        "interpreter": sys.executable,
        "timer": "timeit.Timer.repeat(repeat=7, best-of)",
        "overhead_ns": round(overhead_ns() * 1e9, 2),
        "case_file": "go-engine/testdata/bench_cases.json",
        "note": "同机、单线程、非压测；单次耗时已扣掉 lambda 调用 / timeit 开销",
        "cases": results,
    }
    out = TESTDATA_DIR / "python_bench.json"
    out.write_text(json.dumps(meta, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    print(f"\nraw results -> {out}")


if __name__ == "__main__":
    main()
