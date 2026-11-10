"""用 Python `mahjong` 库生成 Go 引擎的 oracle 用例（M0/M2 对拍基准）。

产出：riichi/testdata/oracle_cases.json
结构：
{
  "meta": {"generator": "tools/oracle/gen_cases.py", "count": N, "seed": 42},
  "shanten": [{"hand": ["1m",...], "shanten": 2}, ...],
  "winning": [{"hand": [...], "winning_tile": "5m", "yaku": [...], "han": 3, "fu": 40, "score": {...}}, ...]
}

用法：
    .venv/Scripts/python.exe tools/oracle/gen_cases.py --count 3000 --seed 42

依赖（dev-only）：
    pip install -i https://pypi.tuna.tsinghua.edu.cn/simple mahjong
"""

from __future__ import annotations

import argparse
import json
import random
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "riichi" / "testdata" / "oracle_cases.json"

SUITS = "mps"
HONORS = ["E", "S", "W", "N", "P", "F", "C"]


def tile_text(kind: int) -> str:
    """把 0..33 的牌号转成 "5m"/"E" 形式的文本。"""
    if kind < 27:
        return f"{kind % 9 + 1}{SUITS[kind // 9]}"
    return HONORS[kind - 27]


def build_wall(rng: random.Random) -> list[int]:
    """136 张牌（0..33 各 4 张），洗牌后返回牌号序列。"""
    wall = [k for k in range(34) for _ in range(4)]
    rng.shuffle(wall)
    return wall


def random_hand(wall: list[int], size: int) -> list[int]:
    """从牌山尾部取 size 张作为手牌（模拟摸牌）。"""
    hand = wall[-size:]
    wall[-size:] = []
    return hand


def main() -> None:
    """生成 shanten 与和牌/点数用例。"""
    parser = argparse.ArgumentParser()
    parser.add_argument("--count", type=int, default=3000)
    parser.add_argument("--seed", type=int, default=42)
    args = parser.parse_args()

    try:
        from mahjong.hand_calculating.hand import HandCalculator
        from mahjong.hand_calculating.hand_config import HandConfig
        from mahjong.shanten import Shanten
        from mahjong.tile import TilesConverter
    except ImportError as exc:  # pragma: no cover - 环境缺依赖时给出明确提示
        raise SystemExit(
            f"缺少 mahjong 库：{exc}\n"
            "请先执行：.venv/Scripts/python.exe -m pip install -i "
            "https://pypi.tuna.tsinghua.edu.cn/simple mahjong"
        ) from exc

    rng = random.Random(args.seed)
    shanten_calc = Shanten()
    calculator = HandCalculator()

    shanten_cases: list[dict] = []
    winning_cases: list[dict] = []

    while len(shanten_cases) < args.count or len(winning_cases) < args.count // 3:
        wall = build_wall(rng)

        # 1) 向听用例：14 张手牌
        hand = random_hand(wall, 14)
        texts = [tile_text(k) for k in hand]
        counts = [0] * 34
        for k in hand:
            counts[k] += 1
        # TilesConverter: 34 数组 -> 136 数组（按张展开）
        tiles_136 = TilesConverter.to_136_tiles([counts[0:9], counts[9:18], counts[18:27], counts[27:34]])
        if len(shanten_cases) < args.count:
            shanten_cases.append({"hand": texts, "shanten": shanten_calc.calculate_shanten(tiles_136)})

        # 2) 和牌/点数用例：13 张 + 1 张和牌
        if len(winning_cases) < args.count // 3:
            base = random_hand(wall, 13)
            win_tile = random_hand(wall, 1)[0]
            full = base + [win_tile]
            counts = [0] * 34
            for k in full:
                counts[k] += 1
            if any(c > 4 for c in counts):
                continue
            tiles_136 = TilesConverter.to_136_tiles(
                [counts[0:9], counts[9:18], counts[18:27], counts[27:34]]
            )
            win_136 = TilesConverter.string_to_136_array(tile_text(win_tile), has_aka_dora=False)[0]
            result = calculator.estimate_hand_value(
                tiles_136,
                win_136,
                config=HandConfig(is_tsumo=True, player_wind=27, round_wind=27),
            )
            if result.error is not None:
                continue
            winning_cases.append(
                {
                    "hand": [tile_text(k) for k in base],
                    "winning_tile": tile_text(win_tile),
                    "yaku": [{"name": y.name, "han": y.han} for y in (result.yaku or [])],
                    "han": result.han,
                    "fu": result.fu,
                    "score": {
                        "main": result.cost["main"] if result.cost else None,
                        "additional": result.cost["additional"] if result.cost else None,
                    },
                }
            )

    payload = {
        "meta": {
            "generator": "tools/oracle/gen_cases.py",
            "library": "mahjong (Python)",
            "count": {"shanten": len(shanten_cases), "winning": len(winning_cases)},
            "seed": args.seed,
        },
        "shanten": shanten_cases,
        "winning": winning_cases,
    }
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text(json.dumps(payload, ensure_ascii=False, indent=1), encoding="utf-8")
    print(f"写入 {OUT}：shanten={len(shanten_cases)} winning={len(winning_cases)}")


if __name__ == "__main__":
    main()
