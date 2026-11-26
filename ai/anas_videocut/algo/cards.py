"""卡片：按画面变化把视频切成卡片、取关键帧（第六版 cards.py 的规则，2026-11-08 #416 定下）。

卡片      只看 2 秒窗口的向量：一个窗口和卡片平均画面的相似度低于 CARD 就另起一张，除非后面 LOOK 个窗口里有一个
          又一样像（A B A 仍是一张）；超过 LONG 秒的卡片在最不像的相邻窗口处切开，两边各至少 SIDE 秒。
关键帧    卡片第一个窗口，之后每个和当前这段平均画面相似度低于 STILL 的窗口；连续 GAP 秒没取到时，每 GAP 秒取一个
          和已取关键帧最不像的窗口；至少 FEWEST 个（不够就均匀取），至多 MOST 个（最像的相邻一对去掉后一个）。
"""

import math
from dataclasses import dataclass

import numpy as np

CARD, LOOK, LONG, SIDE = 0.86, 2, 120.0, 20.0
PIECE_MIN, STILL, GAP, FEWEST, MOST = 2, 0.9, 12.0, 3, 20


@dataclass
class WindowIndex:
    """一组素材的窗口索引：每个视频每 2 秒一个窗口，带关键帧向量和是否纯色（黑场）。由 A-NAS 的视频索引器预先建好，
    adapters/windows.py 读入。videos: {视频: {"file", "windows"}}；rows: (视频, 起, 止)；frames: 单位向量矩阵。"""

    videos: dict
    rows: list
    frames: np.ndarray
    flat: np.ndarray

    @classmethod
    def build(cls, index: dict, vectors: dict) -> "WindowIndex":
        videos = index["videos"]
        rows, frames, flat = [], [], []
        for video, meta in sorted(videos.items()):
            for window in meta["windows"]:
                rows.append((video, window["start"], window["end"]))
                frames.append(vectors[f"{video}/{window['frame']}"])
                flat.append(window["flat"])
        return cls(videos, rows, np.stack(frames), np.array(flat))


def runs(ix: WindowIndex, members: list, tau: float, look: int) -> list[list]:
    """连续的窗口按相似度连成段：低于 tau 的窗口另起一段，除非后面 look 个窗口里有一个又够像；跨过去的窗口不改平均。"""
    out, mean, i = [[members[0]]], ix.frames[members[0]].copy(), 1
    while i < len(members):
        w = members[i]

        def alike(j, mean=mean):
            return float(ix.frames[j] @ mean) / np.linalg.norm(mean) >= tau

        if len(out[-1]) < PIECE_MIN or alike(w):
            out[-1].append(w)
            mean += ix.frames[w]
            i += 1
            continue
        back = next((n for n in range(1, look + 1) if i + n < len(members) and alike(members[i + n])), None)
        if back:
            out[-1].extend(members[i:i + back + 1])
            mean += ix.frames[members[i + back]]
            i += back + 1
            continue
        out.append([w])
        mean = ix.frames[w].copy()
        i += 1
    return out


def split(ix: WindowIndex, w: list) -> list[list]:
    def t(i):
        return ix.rows[i][1]

    if ix.rows[w[-1]][2] - t(w[0]) <= LONG:
        return [w]
    cand = [k for k in range(1, len(w)) if t(w[k]) - t(w[0]) >= SIDE and ix.rows[w[-1]][2] - t(w[k]) >= SIDE]
    if not cand:
        # A long flat gap can leave the whole middle without a usable cut.
        return [w]
    k = min(cand, key=lambda k: float(ix.frames[w[k - 1]] @ ix.frames[w[k]]))
    return split(ix, w[:k]) + split(ix, w[k:])


def card_windows(ix: WindowIndex) -> list[list]:
    """每张卡片的窗口，按视频、时间排列；纯色窗口不进卡片。"""
    by_video = {}
    for i, (video, _s, _e) in enumerate(ix.rows):
        if not ix.flat[i]:
            by_video.setdefault(video, []).append(i)
    out = []
    for video in sorted(by_video):
        members = sorted(by_video[video], key=lambda i: ix.rows[i][1])
        for r in runs(ix, members, CARD, LOOK):
            out += split(ix, r)
    return out


def keyframes(ix: WindowIndex, w: list) -> list:
    """描述卡片用的窗口（见模块说明）。"""
    picked = []
    for run in runs(ix, w, STILL, 0) if len(w) > 1 else [w]:
        picked.append(run[0])
        t0 = ix.rows[run[0]][1]
        for k in range(1, math.ceil((ix.rows[run[-1]][2] - t0) / GAP)):
            chunk = [i for i in run if t0 + k * GAP <= ix.rows[i][1] < t0 + (k + 1) * GAP]
            if chunk:
                picked.append(min(chunk, key=lambda i: max(float(ix.frames[i] @ ix.frames[p]) for p in picked)))
    picked = sorted(set(picked), key=lambda i: ix.rows[i][1])
    while len(picked) > MOST:
        k = max(range(1, len(picked)), key=lambda k: float(ix.frames[picked[k - 1]] @ ix.frames[picked[k]]))
        del picked[k]
    return picked


def plan_cards(ix: WindowIndex, key: str) -> list[dict]:
    """全部卡片，还没有关键帧图片：id、视频、起止、窗口数、关键帧时间、关键帧窗口。"""
    out = []
    for n, w in enumerate(card_windows(ix), 1):
        video, start, end = ix.rows[w[0]][0], ix.rows[w[0]][1], ix.rows[w[-1]][2]
        kf = keyframes(ix, w)
        times = ([max(start, (ix.rows[i][1] + ix.rows[i][2]) / 2) for i in kf] if len(kf) >= FEWEST
                 else [start + (end - start) * (k + 0.5) / FEWEST for k in range(FEWEST)])
        out.append({"id": f"{key}-{n:03d}", "video": video, "start": start, "end": end, "windows": len(w), "times": times,
                    "keys": kf if len(kf) >= FEWEST else []})
    return out


def repeats(beats: list[dict], vecs: list | None) -> list[list[int]]:
    """描述里两个节拍写得一字不差、画面却不像（窗口向量低于 STILL）：标出来。"""
    whats = [b["what"].strip("。. ") for b in beats]
    return [[i + 1, j + 1] for i in range(len(whats)) for j in range(i + 1, len(whats))
            if whats[i] == whats[j] and (vecs is None or float(vecs[i] @ vecs[j]) < STILL)]


def beat_spans(card: dict, beats: list[dict]) -> None:
    """每个节拍的起止：从它的关键帧到下一个关键帧（第一个从卡片开头，最后一个到卡片结尾）。"""
    start, times, end = card["start"], card["times"], card["end"]
    for b, a_, b_ in zip(beats, [start, *times[1:]], [*times[1:], end], strict=True):
        b["start"], b["end"] = round(a_, 2), round(b_, 2)
