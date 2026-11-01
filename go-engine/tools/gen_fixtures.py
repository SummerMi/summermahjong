#!/usr/bin/env python
"""从 Python 版麻将引擎生成 Go 测试 fixture（禁止手写期望值）。

用法（在仓库根目录执行）：

    .venv/Scripts/python.exe go-engine/tools/gen_fixtures.py

生成三个文件：

- ``go-engine/testdata/cases.json``       精选用例：胡牌 / 未胡 / 七对 / 听牌 / 非法输入
- ``go-engine/testdata/random_cases.json`` 2000 条随机手牌及其 Python 判定结果
- （校验）``go-engine/testdata/bench_cases.json`` 只检查张数，保证 benchmark 输入合法

期望值全部由 ``app/services/mahjong/rules.py`` 真实调用得出；fixture 里记录了
Python 版本与两个源文件的 sha256，便于审阅「这份数据确实来自 Python 实现」。
"""

from __future__ import annotations

import hashlib
import json
import random
import sys
from datetime import datetime, timezone
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent
ENGINE_DIR = TOOLS_DIR.parent
REPO_ROOT = ENGINE_DIR.parent
TESTDATA_DIR = ENGINE_DIR / "testdata"

# 允许直接以脚本方式 import 仓库里的 app 包。
sys.path.insert(0, str(REPO_ROOT))

from app.services.mahjong import tiles as tiles_mod  # noqa: E402
from app.services.mahjong.rules import (  # noqa: E402
    is_winning_hand,
    waiting_tiles,
    winning_pattern,
)
from app.services.mahjong.tiles import (  # noqa: E402
    TILE_KINDS,
    InvalidHandError,
    build_deck,
    validate_hand,
)

KIND_COUNT = len(TILE_KINDS)
FIXTURE_SEED = 20260101


def 手牌(text: str) -> list[str]:
    """把 "123m 456p EE" 简写解析成牌面列表（与 tests 里的同名辅助函数一致）。"""
    tiles: list[str] = []
    for group in text.split():
        if group[-1] in {"m", "p", "s"}:
            tiles.extend(f"{rank}{group[-1]}" for rank in group[:-1])
        else:
            tiles.extend(group)
    return tiles


# ---------------------------------------------------------------------------
# 精选用例：输入文本来自 tests/test_mahjong_rules.py，期望值由 Python 引擎现算
# ---------------------------------------------------------------------------

WIN_CASES: list[tuple[str, str]] = [
    # 标准型
    ("standard/四顺子", "123456789m 123p 99s"),
    ("standard/三连刻", "111m 222m 333m 444m 55m"),
    ("standard/四连刻", "111222333444m 55m"),
    ("standard/回溯拆解", "111222333444m 56m"),
    ("standard/边张与将", "123m 123p 123s 789m 99p"),
    ("standard/字牌将", "123m 456m 789p 234s EE"),
    ("standard/字牌刻子", "EEE PPP FFF CCC 11m"),
    ("standard/全顺子", "123m 456m 789m 123p 99s"),
    ("standard/七对撞标准型优先", "11223344556677m"),
    # 七对
    ("seven_pairs/普通七对", "11m 22m 33m 44p 55p 66s EE"),
    ("seven_pairs/四张算两对", "1111m 22p 33p 44s 55s EE"),
    ("seven_pairs/豪华七对", "1111m 2222p 33s 44s EE"),
    # 未胡
    ("not_win/散牌", "1m 4m 7m 1p 4p 7p 1s 4s 7s E S W N P"),
    ("not_win/仅一对", "1m 4m 7m 1p 4p 7p 1s 4s 7s E S W W N"),
    ("not_win/多对子未成胡", "11m 22m 33m 44p 55p 66s 1s 2s"),
    ("not_win/字牌不能组顺", "E S W PPP FFF CCC 11m"),
    ("not_win/顺子不能跨花色", "111m 222p 333s 55p 1m 2m 3p"),
]

WAIT_CASES: list[tuple[str, str]] = [
    ("wait/两面听", "123m 456m 789m 11p 45s"),
    ("wait/边张听", "123m 456m 789m 11p 12s"),
    ("wait/单吊将", "123m 456m 789m 123p 9s"),
    ("wait/双碰听", "123456789m 11p 99s"),
    ("wait/三面听", "12345678m 123p 99s"),
    ("wait/七对听", "11m 22m 33m 44p 55p 66s E"),
    ("wait/豪华七对听", "1111m 22p 33p 44s 55s E"),
    ("wait/双碰顺序", "123m 456m 789m 9p 9p 9s 9s"),
    ("wait/九莲宝灯九面听", "1112345678999m"),
    ("wait/不听牌", "1m 4m 7m 1p 4p 7p 1s 4s 7s E S W N"),
    ("wait/已有四张不推荐", "123m 456m 789m 9999s"),
]

# 非法输入：check 表示调用哪个接口，期望抛 InvalidHandError。
INVALID_CASES: list[tuple[str, str, list[str]]] = [
    *(("invalid/win-张数%s" % size, "win", list(TILE_KINDS[:size])) for size in (0, 1, 13, 15)),
    *(("invalid/wait-张数%s" % size, "wait", list(TILE_KINDS[:size])) for size in (0, 1, 12, 14)),
    ("invalid/未知牌", "win", 手牌("123m 456m 789m 11p 99s") + ["0m"]),
    (
        "invalid/同种超四张-win",
        "win",
        ["1m"] * 5 + ["2m", "3m", "4m", "5p", "6p", "7p", "8s", "9s", "E"],
    ),
    (
        "invalid/同种超四张-wait",
        "wait",
        ["1m"] * 5 + ["2m", "3m", "4m", "5p", "6p", "7p", "8s", "9s"],
    ),
]


def build_curated_cases() -> tuple[list[dict], dict]:
    """用 Python 引擎现算精选用例的期望值。"""
    cases: list[dict] = []

    for case_id, text in WIN_CASES:
        tiles = 手牌(text)
        assert len(tiles) == 14, (case_id, len(tiles))
        pattern = winning_pattern(tiles)
        is_win = is_winning_hand(tiles)
        assert is_win == (pattern is not None), case_id
        cases.append(
            {
                "id": case_id,
                "check": "win",
                "hand": text,
                "tiles": tiles,
                "size": len(tiles),
                "pattern": pattern,
                "is_win": is_win,
                "waits": None,
                "error": None,
            }
        )

    for case_id, text in WAIT_CASES:
        tiles = 手牌(text)
        assert len(tiles) == 13, (case_id, len(tiles))
        waits = waiting_tiles(tiles)
        cases.append(
            {
                "id": case_id,
                "check": "wait",
                "hand": text,
                "tiles": tiles,
                "size": len(tiles),
                "pattern": None,
                "is_win": None,
                "waits": waits,
                "error": None,
            }
        )

    for case_id, check, tiles in INVALID_CASES:
        try:
            if check == "win":
                winning_pattern(tiles)
            else:
                waiting_tiles(tiles)
        except InvalidHandError as exc:  # 期望路径
            cases.append(
                {
                    "id": case_id,
                    "check": check,
                    "hand": None,
                    "tiles": tiles,
                    "size": len(tiles),
                    "pattern": None,
                    "is_win": None,
                    "waits": None,
                    "error": type(exc).__name__,
                }
            )
        else:  # pragma: no cover - 生成脚本自检
            raise AssertionError(f"{case_id} 期望抛 InvalidHandError，实际没有")

    stats = {
        "total": len(cases),
        "win_check": sum(1 for c in cases if c["check"] == "win"),
        "wait_check": sum(1 for c in cases if c["check"] == "wait"),
        "standard": sum(1 for c in cases if c["pattern"] == "standard"),
        "seven_pairs": sum(1 for c in cases if c["pattern"] == "seven_pairs"),
        "not_win": sum(1 for c in cases if c["check"] == "win" and c["pattern"] is None and not c["error"]),
        "invalid": sum(1 for c in cases if c["error"]),
        "wait_empty": sum(1 for c in cases if c["check"] == "wait" and c["waits"] == []),
    }
    return cases, stats


# ---------------------------------------------------------------------------
# 随机用例：一半纯随机手牌，一半「构造出来的胡牌 / 听牌手牌」，覆盖正样本
# ---------------------------------------------------------------------------


def random_uniform_hand(rng: random.Random, size: int) -> list[str]:
    """从完整牌墙随机抽 size 张（天然合法）。"""
    wall = build_deck()
    rng.shuffle(wall)
    return wall[:size]


def random_winning_hand(rng: random.Random) -> list[str]:
    """构造一手标准型胡牌：4 个面子 + 1 个将，且每种牌不超过 4 张。"""
    for _ in range(500):
        counts = [0] * KIND_COUNT
        ok = True
        for _ in range(4):
            if rng.random() < 0.5:
                kind = rng.randrange(KIND_COUNT)
                if counts[kind] > 1:
                    ok = False
                    break
                counts[kind] += 3
            else:
                suit = rng.randrange(3)
                start = suit * 9 + rng.randrange(7)
                add = (start, start + 1, start + 2)
                if any(counts[k] > 3 for k in add):
                    ok = False
                    break
                for k in add:
                    counts[k] += 1
        if not ok:
            continue
        pair_candidates = [k for k in range(KIND_COUNT) if counts[k] <= 2]
        pair = rng.choice(pair_candidates)
        counts[pair] += 2
        return [TILE_KINDS[k] for k in range(KIND_COUNT) for _ in range(counts[k])]
    raise RuntimeError("构造胡牌手牌失败")  # pragma: no cover


def random_seven_pairs_hand(rng: random.Random) -> list[str]:
    """构造七对：a 组四张相同 + b 组不同对子，满足 2a + b = 7。"""
    four_kinds = rng.choice([0, 0, 1, 1, 2])
    pair_kinds = 7 - 2 * four_kinds
    kinds = rng.sample(range(KIND_COUNT), four_kinds + pair_kinds)
    fours, pairs = kinds[:four_kinds], kinds[four_kinds:]
    tiles: list[str] = []
    for kind in fours:
        tiles.extend([TILE_KINDS[kind]] * 4)
    for kind in pairs:
        tiles.extend([TILE_KINDS[kind]] * 2)
    return tiles


def random_mutated_hand(rng: random.Random, tiles: list[str]) -> list[str]:
    """随机替换一张牌，尽量保持合法；用来制造「看起来像胡其实不是」的手牌。"""
    hand = list(tiles)
    for _ in range(100):
        position = rng.randrange(len(hand))
        trial = list(hand)
        trial[position] = rng.choice(TILE_KINDS)
        try:
            validate_hand(trial)
        except InvalidHandError:
            continue
        return trial
    return hand  # pragma: no cover - 兜底


def make_random_case(case_id: str, kind: str, tiles: list[str]) -> dict:
    """调用 Python 引擎得出期望值。"""
    if len(tiles) == 14:
        pattern = winning_pattern(tiles)
        is_win = is_winning_hand(tiles)
        assert is_win == (pattern is not None), case_id
        return {
            "id": case_id,
            "kind": kind,
            "check": "win",
            "tiles": tiles,
            "size": 14,
            "pattern": pattern,
            "is_win": is_win,
        }
    assert len(tiles) == 13, (case_id, len(tiles))
    waits = waiting_tiles(tiles)
    return {
        "id": case_id,
        "kind": kind,
        "check": "wait",
        "tiles": tiles,
        "size": 13,
        "waits": waits,
    }


def build_random_cases() -> tuple[list[dict], dict]:
    """生成 2000 条随机用例（含正负样本），全部由 Python 引擎判定。"""
    rng = random.Random(FIXTURE_SEED)
    cases: list[dict] = []
    seen: set[tuple[str, ...]] = set()

    def add(kind: str, tiles: list[str]) -> None:
        key = tuple(tiles)
        if key in seen:
            return
        seen.add(key)
        cases.append(make_random_case(f"random/{len(cases) + 1:04d}", kind, tiles))

    target = 2000
    while len(cases) < target:
        remaining = target - len(cases)
        # 每轮按配额补样本，最后一个配额吃掉余量，保证总数恰为 2000。
        quotas = [
            ("uniform-14", 400, lambda: random_uniform_hand(rng, 14)),
            ("uniform-13", 300, lambda: random_uniform_hand(rng, 13)),
            ("constructed-standard-14", 400, lambda: random_winning_hand(rng)),
            ("constructed-seven-pairs-14", 200, lambda: random_seven_pairs_hand(rng)),
            (
                "constructed-wait-13",
                300,
                lambda: rng.choice(
                    [random_winning_hand(rng), random_seven_pairs_hand(rng)]
                )[:-1],
            ),
            (
                "mutated-14",
                200,
                lambda: random_mutated_hand(rng, random_winning_hand(rng)),
            ),
            (
                "constructed-seven-pairs-wait-13",
                200,
                lambda: random_seven_pairs_hand(rng)[:-1],
            ),
        ]
        for kind, quota, factory in quotas:
            want = min(quota, remaining)
            produced = 0
            attempts = 0
            while produced < want and attempts < want * 200:
                attempts += 1
                before = len(cases)
                add(kind, factory())
                if len(cases) > before:
                    produced += 1
            remaining = target - len(cases)
            if remaining <= 0:
                break
        if len(cases) < target:
            # 配额都补过一轮仍不够（例如去重后数量不足），直接补纯随机手牌。
            add("uniform-backfill", random_uniform_hand(rng, rng.choice([13, 14])))

    by_kind: dict[str, int] = {}
    for case in cases:
        by_kind[case["kind"]] = by_kind.get(case["kind"], 0) + 1
    stats = {
        "total": len(cases),
        "by_kind": by_kind,
        "win": sum(1 for c in cases if c["check"] == "win" and c["is_win"]),
        "not_win": sum(1 for c in cases if c["check"] == "win" and not c["is_win"]),
        "wait_check": sum(1 for c in cases if c["check"] == "wait"),
        "wait_nonempty": sum(1 for c in cases if c["check"] == "wait" and c["waits"]),
        "wait_empty": sum(1 for c in cases if c["check"] == "wait" and not c["waits"]),
    }
    return cases, stats


# ---------------------------------------------------------------------------
# 元信息与落盘
# ---------------------------------------------------------------------------


def sha256_of(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def make_meta(stats: dict) -> dict:
    return {
        "generator": "go-engine/tools/gen_fixtures.py",
        "python_version": sys.version.split()[0],
        "generated_at_utc": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "tile_kinds": list(TILE_KINDS),
        "source_sha256": {
            "app/services/mahjong/tiles.py": sha256_of(REPO_ROOT / "app/services/mahjong/tiles.py"),
            "app/services/mahjong/rules.py": sha256_of(REPO_ROOT / "app/services/mahjong/rules.py"),
        },
        "expectations_computed_by": "app.services.mahjong.rules",
        "stats": stats,
    }


def write_json(path: Path, payload: dict) -> None:
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    print(f"已写入 {path}（{path.stat().st_size} 字节）")


def check_bench_cases() -> None:
    """校验 benchmark 用例（输入是手写的，但张数必须与接口一致）。"""
    payload = json.loads((TESTDATA_DIR / "bench_cases.json").read_text(encoding="utf-8"))
    for group, expected_size in (("win", 14), ("wait", 13)):
        for case in payload[group]:
            tiles = 手牌(case["hand"])
            assert len(tiles) == expected_size, (case["id"], len(tiles))
            validate_hand(tiles, allowed_lengths=(expected_size,))
    print(
        "bench_cases.json 校验通过："
        f"{len(payload['win'])} 手 14 张 + {len(payload['wait'])} 手 13 张"
    )


def main() -> None:
    curated, curated_stats = build_curated_cases()
    random_cases, random_stats = build_random_cases()
    check_bench_cases()

    curated_meta = make_meta(curated_stats)
    random_meta = make_meta(random_stats)
    random_meta["seed"] = FIXTURE_SEED

    write_json(TESTDATA_DIR / "cases.json", {"meta": curated_meta, "cases": curated})
    write_json(TESTDATA_DIR / "random_cases.json", {"meta": random_meta, "cases": random_cases})

    print("精选用例统计:", json.dumps(curated_stats, ensure_ascii=False))
    print("随机用例统计:", json.dumps(random_stats, ensure_ascii=False))


if __name__ == "__main__":
    main()
