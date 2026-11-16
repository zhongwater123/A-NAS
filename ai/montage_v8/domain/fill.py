"""把第二步挑的节拍填进音乐槽，得到成片时间线（第六版 cut.cut_to_music 的计算部分）。

每个画面的槽按它的节拍顺序填，一个槽一个节拍，从关键帧起取槽的长度（卡片先结束就在卡片里往前挪；卡片太短就把槽
剩下的给下一个节拍，但不留不到 PIECE_LEAST 的碎片）。画面自己的卡片填不满时，从拍摄时间相邻、画面最像的卡片补。
"""

from collections.abc import Callable
from dataclasses import dataclass

import numpy as np

from .cards import WindowIndex
from .plan import RUN, RUN_PLAIN, SMALL_RUN

PIECE_LEAST = 1.5    # 秒：成片里不留比这更短的镜头
NEAR = 6             # 填不满的槽从拍摄顺序前后各 NEAR 张卡片里补
SLOW_MOST = 2.5      # 结尾镜头最多放慢到这么多倍来填满槽
SMALL = {"中景", "近景", "特写"}

# held(节拍, 视频文件, 关键帧秒数, lo, hi, 是否翻转) -> (lo, hi)：关键帧两侧没有东西挡镜头的区间（picture.clear，结果缓存）。
Held = Callable[[str, str, float, float, float, bool], tuple[float, float]]


def breaks_run(clips: list[tuple], video: str, size: str, best: set, labelled: bool) -> bool:
    """在 clips（已有镜头的 (视频, 景别)）后面再接一个 video 的镜头（景别 size）是否超出连续上限。
    best：有精彩节拍的视频；labelled：这次剪辑带人标精彩度。"""
    run = small = 0
    for v, z in reversed(clips):
        if v != video:
            break
        run += 1
        small += small == run - 1 and z in SMALL
    limit = RUN if not labelled or video in best else RUN_PLAIN
    return run >= limit or (size in SMALL and small >= SMALL_RUN)


@dataclass
class Footage:
    """填槽要用的素材信息。cards：全部描述过的卡片 {id: 卡片}；catalog：目录里卡片的 id，按拍摄顺序；
    index：窗口索引（算卡片的平均画面）；source(视频) -> 视频文件路径；turned：要翻转回来的视频文件。"""

    cards: dict
    catalog: list
    index: WindowIndex
    source: Callable[[str], str]
    turned: set


def fill(m: dict, planned: dict, picked: dict, footage: Footage, held: Held, *, fps: int, labels: dict | None = None
         ) -> tuple[list[dict], list[tuple]]:
    """(clips, short)：成片的镜头和没填满的槽。planned：shots-*.json；picked：第一步的回答。"""
    labels = labels or {}
    cards, catalog, ix = footage.cards, footage.catalog, footage.index
    seen_as = {}

    def picture(cid):
        """卡片的平均画面：它覆盖的索引窗口的向量平均。"""
        if cid not in seen_as:
            c = cards[cid]
            rows = [i for i, (v, a, b) in enumerate(ix.rows) if v == c["video"] and a < c["end"] and b > c["start"]]
            v = ix.frames[rows].mean(axis=0)
            seen_as[cid] = v / np.linalg.norm(v)
        return seen_as[cid]

    def neighbours(ids):
        at = [catalog.index(i) for i in ids if i in catalog]
        if not at:
            return []
        near = sorted({catalog[k] for i in at for k in range(max(0, i - NEAR), min(len(catalog), i + NEAR + 1))} - set(ids))
        like = sum(picture(i) for i in ids if i in cards)
        near.sort(key=lambda cid: -float(picture(cid) @ like))
        return [f"{cid}/{n}" for cid in near for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]

    def size(cid, j):
        return cards[cid]["answer"]["beats"][int(j) - 1]["shot_size"]

    best = {cards[b.rsplit("/", 1)[0]]["video"] for b, mark in labels.items() if mark == "3" and b.rsplit("/", 1)[0] in cards}
    clips, taken, short = [], [], []
    many = len(planned["answer"]["parts"])
    for p, (part, step1, slots_p) in enumerate(zip(planned["answer"]["parts"], picked["parts"], planned["slots"], strict=True), 1):
        level = m["parts"][p - 1]["level"]
        for k, (pic, slots) in enumerate(zip(part["pictures"], slots_p, strict=True), 1):
            # 结尾画面选的节拍收尾：它的槽从最后一个往前填，这个节拍占最后一个槽，补这个槽的放在它前面。
            ending = p == many and k == len(part["pictures"])
            mine = len(clips)
            listed = [b for b in dict.fromkeys(pic["beats"]) if b.rsplit("/", 1)[0] in cards]
            pool = [f"{cid}/{n}" for cid in dict.fromkeys(b.rsplit("/", 1)[0] for b in listed)
                    for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]
            if listed:
                c0, j0 = listed[0].rsplit("/", 1)
                anchor = cards[c0]["times"][int(j0) - 1]
                pool.sort(key=lambda b: abs(cards[b.rsplit("/", 1)[0]]["times"][int(b.rsplit("/", 1)[1]) - 1] - anchor))
            order = listed + [b for b in pool if b not in listed]
            # 然后是第一步给这个画面的其他卡片，最后才是和它们拍摄时间相邻的卡片。
            named = [f"{cid}/{n}" for cid in step1["scenes"][k - 1]["cards"] if cid in cards
                     for n in range(1, len(cards[cid]["answer"]["beats"]) + 1)]
            order += [b for b in named if b not in order]
            order += neighbours(list(dict.fromkeys(b.rsplit("/", 1)[0] for b in order)))
            for a, b in reversed(slots) if ending else slots:
                need, first = round(b * fps) - round(a * fps), True
                # 结尾的槽倒着填、填完再排顺序：那里不数连续。
                so_far = [(x["video"], size(x["card"], x["beats"][0])) for x in clips]
                fits = [] if ending else [bt for bt in order if not breaks_run(
                    so_far, cards[bt.rsplit("/", 1)[0]]["video"], size(*bt.rsplit("/", 1)), best, bool(labels))]
                for beat in fits + [bt for bt in order if bt not in fits]:
                    if need <= 0:
                        break
                    cid, j = beat.rsplit("/", 1)
                    c = cards[cid]
                    # 关键帧（描述看到的那一帧）放在镜头三分之一处，不放在开头。
                    seen = c["times"][int(j) - 1]
                    if any(v == cid and x - 0.01 <= seen < y - 0.01 for v, x, y in taken):
                        continue
                    file = footage.source(c["video"])
                    lo, hi = held(beat, file, seen, max(c["start"], seen - need / fps), min(c["end"], seen + need / fps),
                                  file in footage.turned)
                    start = max(lo, seen - need / fps / 3)
                    start = max([start] + [y for v, _x, y in taken if v == cid and y <= seen + 0.01])
                    room = min([hi] + [x for v, x, _y in taken if v == cid and x > start])
                    frames = min(need, int((room - start) * fps))
                    slow = 1.0
                    if frames < need and first:      # 一个节拍填整个槽：在卡片里往前挪
                        earliest = max([lo] + [y for v, _x, y in taken if v == cid and y <= start + 0.01])
                        if room - earliest >= need / fps:
                            start, frames = room - need / fps, need
                        else:
                            start, frames = earliest, min(need, int((room - earliest) * fps))
                    shown = frames
                    if ending and first and a == slots[-1][0] and 0 < frames < need:
                        # 结尾自己的节拍不够最后一个槽：放慢拉长，不接邻居。
                        shown = min(need, int(frames * SLOW_MOST))
                        slow = shown / frames
                    # 不留不到 PIECE_LEAST 的碎片：会剩下太短的一段时，这个镜头让出一些给后面的。
                    least = round(PIECE_LEAST * fps)
                    if shown < need and need - shown < least and shown - (least - (need - shown)) >= least:
                        shown -= least - (need - shown)
                        frames = min(frames, shown)
                        slow = shown / frames
                    if shown < min(need, least):
                        continue
                    end = start + frames / fps
                    clips.append({"card": cid, "beats": [int(j)], "start": start, "end": end, "video": c["video"],
                                  "key": c["times"][int(j) - 1], "part": p, "partName": part["name"], "picture": k,
                                  "level": level, "slot": [a, b], "query": step1["scenes"][k - 1]["query"],
                                  "shows": pic["shows"], "link": pic["link"], "share": round(shown / fps, 3)}
                                 | ({"slow": round(slow, 3)} if slow != 1 else {}))
                    taken.append((cid, start, end))
                    need -= shown
                    first = False
                if need > 0:
                    short.append((p, k, a, b, need))
            if ending and len(clips) > mine:
                last = clips[mine]       # 最后一个槽的第一段是选的节拍；补的按拍摄顺序
                rest = sorted(clips[mine + 1:], key=lambda c: (c["slot"][0], catalog.index(c["card"]) if c["slot"] == last["slot"] and c["card"] in catalog else 0))
                clips[mine:] = [*rest, last]
    for c in clips:
        beats = cards[c["card"]]["answer"]["beats"]
        c |= {"beat": f"{c['card']}/{c['beats'][0]}", "frame": cards[c["card"]]["frames"][c["beats"][0] - 1],
              "shotSize": beats[c["beats"][0] - 1]["shot_size"], "what": beats[c["beats"][0] - 1]["what"],
              "weight": f"画面{c['picture']}"}
    return clips, short


def seconds(clips: list[dict]) -> float:
    return sum((c["end"] - c["start"]) * c.get("slow", 1.0) for c in clips)
