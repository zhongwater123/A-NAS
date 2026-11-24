"""画质规则：按 quality-<case>.json 里量好的数值决定哪些卡片不进目录（第六版 cut.poor）。

模糊单独不能说明问题：用户留下了 8–10.8 的静止日落和黄昏，去掉的是镜头移动时糊掉的（抖动 2–4.4），所以模糊超过
BLUR_MOST 且抖动超过 BLUR_MOVING 才算。抖动 3–5.5 像素两种都有，超过 SHAKE_MOST 的全部去掉。黑帧（量不出来）和
遮挡占比 BLOCKED_MOST 及以上也去掉。在用户标过的 36 张上：留下的 22 张一张没去，该去的 14 张去了 10 张。
"""

import math
import statistics

BLUR_MOST, BLUR_MOVING, SHAKE_MOST, BLOCKED_MOST = 8.0, 1.0, 5.6, 0.25
# 夜空失焦（2026-11-11）：多数关键帧是暗的，且每百万像素的锐点中位数低于 POINTS_LEAST（ne-070 的软星 22–27，
# 对焦的夜景 150 以上）。
POINTS_LEAST = 60


def out_of_focus(points: list) -> bool:
    """多数关键帧是暗的（有数），且锐点少。"""
    dark = [p for p in points if p is not None]
    return len(dark) * 2 > len(points) and statistics.median(dark) < POINTS_LEAST


def reasons(q: dict) -> list[str]:
    """一张卡片画质上不能用的原因，没有就是空的。"""
    return [w for w, bad in (("黑帧", math.isnan(q["blur"])), ("模糊", q["blur"] > BLUR_MOST and q["shake"] > BLUR_MOVING),
                             ("抖动", q["shake"] > SHAKE_MOST), ("遮挡", q["blocked"] >= BLOCKED_MOST),
                             ("失焦", out_of_focus(q.get("points", [])))) if bad]


def poor(measures: dict, kept: set) -> dict:
    """{卡片: [原因]}：画质规则要去掉的卡片，人工复核保留的（reviewed.json）除外。"""
    out = {}
    for cid, q in measures.items():
        if cid in kept:
            continue
        why = reasons(q)
        if why:
            out[cid] = why
    return out
