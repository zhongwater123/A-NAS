"""A set's cards, cut and described the way settled on 2026-11-08 (#416), then kept for 第六版:

cards      2 s windows alone (no FFmpeg cuts): a window under CARD 0.86 to the card's mean starts the
           next card unless one of the next LOOK 2 windows is that alike again (A B A stays one card);
           a card past LONG 120 s is cut at its least alike neighbouring windows, SIDE 20 s a side;
key frames the card's first window, then each window under STILL 0.9 to the mean of the run so far; a run
           going GAP 12 s without one gets, every 12 s, the window least like the key frames taken; at
           least 3 (else 3 spread evenly), at most 20 (the most alike neighbouring pair loses its later frame);
described  by Qwen3.5-4B (tag_beats.describe_v2), one beat a key frame, each frame 512 wide; only the cards
           quality.py does not leave out (cut.poor): the rest keep an error and are never read.

  python cards.py frames CASE ...         cards and key frames (no model): cards-<case>.json under paths.CARDS
  python quality.py CASE ...              blur, shake, blocked, points: quality-<case>.json
  python cards.py describe URL CASE ...   the 4B's descriptions (llama-server at URL); resumes
"""
import math
import subprocess
import sys
import time

import cut
import numpy as np
import tag_beats
import windows
from paths import CARDS as HERE, VIDEOS, read_json, write_json

CARD, LOOK, LONG, SIDE = 0.86, 2, 120.0, 20.0
PIECE_MIN, STILL, GAP, FEWEST, MOST = 2, 0.9, 12.0, 3, 20


def runs(ix, members, tau, look):
    """Index._pieces with look-ahead: a window under tau to the run's mean joins it when one of the
    next look windows is tau alike to that mean; bridged windows do not move the mean."""
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


def split(ix, w):
    t = lambda i: ix.rows[i][1]  # noqa: E731
    if ix.rows[w[-1]][2] - t(w[0]) <= LONG:
        return [w]
    cand = [k for k in range(1, len(w)) if t(w[k]) - t(w[0]) >= SIDE and ix.rows[w[-1]][2] - t(w[k]) >= SIDE]
    k = min(cand, key=lambda k: float(ix.frames[w[k - 1]] @ ix.frames[w[k]]))
    return split(ix, w[:k]) + split(ix, w[k:])


def card_windows(ix):
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


def keyframes(ix, w):
    """The windows to describe a card from (see the module's doc)."""
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


def grab(case, video, path, t):
    out = HERE / "frames" / case / video / f"{round(t * 1000):07d}.jpg"
    if not out.exists():
        out.parent.mkdir(parents=True, exist_ok=True)
        # A seek within a frame of the file's end gives none (a DJI clip, 0.02 s before it): back off.
        for at in (t, t - 0.5, t - 1.0):
            done = subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", f"{max(0.0, at):.3f}", "-i", str(path),  # noqa: S607
                                   "-frames:v", "1", "-vf", "scale=512:-2", "-q:v", "3", "-y", str(out)], check=False)
            if done.returncode == 0 and out.exists():
                break
        else:
            raise RuntimeError(f"no frame at {t:.3f} s of {path}")
    return out


def frames(case):
    """Every card of the set with its key frames grabbed; descriptions kept where they were made."""
    ix = windows.Index.load(str(cut.index_dir(case) / "index.json"), str(cut.index_dir(case) / "frames.npz"))
    path = HERE / f"cards-{case}.json"
    done = read_json(path) if path.exists() else {}
    for n, w in enumerate(card_windows(ix), 1):
        cid = f"{case}-{n:03d}"
        if cid in done:
            continue
        video, start, end = ix.rows[w[0]][0], ix.rows[w[0]][1], ix.rows[w[-1]][2]
        kf = keyframes(ix, w)
        times = ([max(start, (ix.rows[i][1] + ix.rows[i][2]) / 2) for i in kf] if len(kf) >= FEWEST
                 else [start + (end - start) * (k + 0.5) / FEWEST for k in range(FEWEST)])
        files = [grab(case, video, VIDEOS / ix.videos[video]["file"], t) for t in times]
        done[cid] = {"id": cid, "video": video, "start": start, "end": end, "windows": len(w), "times": times,
                     "frames": [str(f) for f in files], "keys": kf if len(kf) >= FEWEST else []}
        write_json(done, path)
    print(case, len(done), "张卡片", flush=True)


def describe(case, url):
    """The 4B's description of each card quality.py keeps; two beats said alike over frames that are not
    (window vectors under STILL) are flagged as repeats."""
    ix = windows.Index.load(str(cut.index_dir(case) / "index.json"), str(cut.index_dir(case) / "frames.npz"))
    path = HERE / f"cards-{case}.json"
    done = read_json(path)
    bad = cut.poor(case)
    for cid, c in done.items():
        if "answer" in c:
            continue
        if cid in bad:
            c["error"] = "画面质量差，未描述：" + "、".join(bad[cid])
            write_json(done, path)
            continue
        start, end, times = c["start"], c["end"], c["times"]
        t0 = time.perf_counter()
        try:
            answer, score, seconds, errors = tag_beats.describe_v2(
                c["frames"], [t - start for t in times], list(range(1, len(times) + 1)), end - start, url)
            for b, a_, b_ in zip(answer["beats"], [start, *times[1:]], [*times[1:], end], strict=True):
                b["start"], b["end"] = round(a_, 2), round(b_, 2)
            vecs = [ix.frames[i] for i in c.get("keys", [])] or None
            whats = [b["what"].strip("。. ") for b in answer["beats"]]
            flags = [[i + 1, j + 1] for i in range(len(whats)) for j in range(i + 1, len(whats))
                     if whats[i] == whats[j] and (vecs is None or float(vecs[i] @ vecs[j]) < STILL)]
            c.pop("error", None)
            c |= {"answer": answer, "score": score, "seconds": round(seconds, 1), "errors": errors, "repeats": flags}
        except Exception as error:  # noqa: BLE001  kept as the card's result
            c |= {"error": str(error), "seconds": round(time.perf_counter() - t0, 1)}
        write_json(done, path)
        print(cid, f"{end - start:.0f}s", len(times), "帧", c["seconds"], "秒", c.get("answer", {}).get("summary", c.get("error")), flush=True)


if __name__ == "__main__":
    if sys.argv[1] == "frames":
        for case in sys.argv[2:]:
            frames(case)
    elif sys.argv[1] == "describe":
        for case in sys.argv[3:]:
            describe(case, sys.argv[2])
    else:
        sys.exit(__doc__)
