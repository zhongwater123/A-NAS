"""把第二步挑的节拍填进音乐槽，得到成片时间线（第六版 cut.cut_to_music 的计算部分）。

每个画面的槽按它的节拍顺序填，一个槽一个节拍，从关键帧起取槽的长度（卡片先结束就在卡片里往前挪；卡片太短就把槽
剩下的给下一个节拍，但不留不到 PIECE_LEAST 的碎片）。画面自己的卡片填不满时，从拍摄时间相邻、画面最像的卡片补。
填完后一个画面的几个槽按拍摄先后排（in_shooting_order）。拼接过的素材（Footage.parts），一个镜头不跨过拼接点。
"""

from collections.abc import Callable
from dataclasses import dataclass, field

import numpy as np

from .cards import WindowIndex
from .music import one_take
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
    index：窗口索引（算卡片的平均画面）；source(视频) -> 视频文件路径；turned：要翻转回来的视频文件；
    parts：拼接过的视频由哪些原片拼成（joined-parts.json：{视频: [{file, at, duration}]}，at 是原片在拼接文件里的起点）。"""

    cards: dict
    catalog: list
    index: WindowIndex
    source: Callable[[str], str]
    turned: set
    parts: dict = field(default_factory=dict)


def in_shooting_order(pieces: list[dict], slots: list, fps: int, rank: Callable[[str], int]) -> list[dict]:
    """一个画面的镜头按拍摄先后排（rank：卡片在拍摄顺序里的位置），不管第二步把节拍排成什么顺序。（2026-11-17，
    用户：潜水 dv-020 从 13 秒跳回 2 秒；同一段录像就该按先后放，第二步排第一的、开头和结尾的画面也一样。）每个槽连同
    补它的镜头和它的长度整个挪动；槽都是整小节，怎么排切点都在小节线上（潜水拼接第 1 版的副歌是 345.MOV 的 5.8、5.8、
    3.9 秒，原来放成 36、11、0 秒）。"""
    groups = [[x for x in pieces if x["slot"] == list(s)] for s in slots]
    if len(groups) < 2 or not all(groups):
        return pieces
    order = sorted(range(len(groups)), key=lambda n: (rank(groups[n][0]["card"]), groups[n][0]["start"]))
    frame = round(slots[0][0] * fps)
    for n in order:
        a, b = slots[n]
        end = frame + round(b * fps) - round(a * fps)
        for x in groups[n]:
            x["slot"] = [frame / fps, end / fps]
        frame = end
    return [x for n in order for x in groups[n]]


def fill(m: dict, planned: dict, picked: dict, footage: Footage, held: Held, *, fps: int, labels: dict | None = None
         ) -> tuple[list[dict], list[tuple]]:
    """(clips, short)：成片的镜头和没填满的槽。planned：shots-*.json；picked：第一步的回答。"""
    labels = labels or {}
    cards, catalog, ix, joined = footage.cards, footage.catalog, footage.index, footage.parts
    seen_as = {}

    def source(cid):
        """连续镜头按原片数：拼接文件按卡片所在的那条原片（一个拼接文件能长达几个小时）。"""
        c = cards[cid]
        at = [q["file"] for q in joined.get(c["video"], []) if q["at"] <= (c["start"] + c["end"]) / 2]
        return f"{c['video']}:{at[-1]}" if at else c["video"]

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
        # 拼接过的素材卡片少而长，前后各 NEAR 张能远到几个小时以外（早餐里补进一家商店）：只取紧挨着的卡片。
        r = 1 if joined else NEAR
        near = sorted({catalog[k] for i in at for k in range(max(0, i - r), min(len(catalog), i + r + 1))} - set(ids))
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
            slots = one_take(slots, level)       # one_take 之前规划的高潮段还是一小节一个槽
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
                so_far = [(source(x["card"]), size(x["card"], x["beats"][0])) for x in clips]
                fits = [] if ending else [bt for bt in order if not breaks_run(
                    so_far, source(bt.rsplit("/", 1)[0]), size(*bt.rsplit("/", 1)), best, bool(labels))]
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
                    # 2026-11-17（用户）：拼接素材的镜头不出关键帧所在的那条原片；拼接点是硬切点，卡片是跨着它拼起来的。
                    seams = [q["at"] for q in joined.get(c["video"], [])]
                    lo = max([lo] + [x for x in seams if x <= seen])
                    hi = min([hi] + [x for x in seams if x > seen])
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
            clips[mine:] = in_shooting_order(clips[mine:], slots, fps,
                                             lambda cid: catalog.index(cid) if cid in catalog else len(catalog))
    for c in clips:
        beats = cards[c["card"]]["answer"]["beats"]
        c |= {"beat": f"{c['card']}/{c['beats'][0]}", "frame": cards[c["card"]]["frames"][c["beats"][0] - 1],
              "shotSize": beats[c["beats"][0] - 1]["shot_size"], "what": beats[c["beats"][0] - 1]["what"],
              "weight": f"画面{c['picture']}"}
    return clips, short


def seconds(clips: list[dict]) -> float:
    return sum((c["end"] - c["start"]) * c.get("slow", 1.0) for c in clips)
