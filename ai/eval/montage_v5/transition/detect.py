"""Moments in the footage that could carry a transition, found by rule (2026-11-16, the user: can we find
them?). What travel editors cut on: a whip pan (the picture smears sideways), something passing at the lens
(a hand, a person, a pole), a push in or pull out, a tilt up to the sky or down to the ground.

Each video is read at FPS frames a second, grey and SIZE (FFmpeg). For each pair of frames:

  move    the shift of each of GRID x GRID patches (phase correlation, quality.shift, no OpenCV), fitted with
          a pan (tx, ty), a scale rate (zoom: patches move out from the centre) and a turn; pixels of a
          320-wide frame per frame, the scale as a share of the frame per frame.
  sharp   the mean absolute Laplacian: a whip pan smears it away.
  soft    the share of the frame in soft blocks (held.soft_blocks: out of focus or flat, as a hand at the lens
          or a clear sky is); dark, the share under DARK.

Rules (first guesses; the user's marks on the page say how good they are):

  whip    the pan at least WHIP for two frames running, or at least WHIP_SMEARED while sharp falls under
          SMEAR of the video's median.
  cover   soft or dark, less its median over the COVER_BEFORE seconds before, at least COVER, for at most
          COVER_LONGEST seconds (it comes and goes).
  zoom    the scale rate summed over ZOOM_SPAN seconds at least ZOOM (in) or at most -ZOOM (out), ZOOM_SAME of
          its frames that way and the fit's median error under ZOOM_ERR. (The first try, 0.12 alone, found 383
          in 47 minutes: the scale is noisy, 0.26 at the 90th percentile, and a car driving on spreads it too.)
  tilt    the vertical pan summed over TILT_SPAN seconds at least TILT, ending on soft at least SKY (sky or
          ground fills the picture). (60 and 0.55 found none.)

Writes signals (per video) and events to OUT; each event with its video, time, rule and direction.

  python3 transition/detect.py OUT.json [CASE]
  python3 transition/detect.py OUT.json --events      (the rules again, on the signals in OUT)
"""
import json
import subprocess
import sys
from pathlib import Path

import numpy as np

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))  # montage_v5
import cut  # noqa: E402
import held  # noqa: E402
import windows  # noqa: E402
from paths import VIDEOS, read_json  # noqa: E402
from quality import shift  # noqa: E402

FPS, SIZE, GRID = 10, (320, 180), 3
DARK = 25
WHIP, WHIP_SMEARED, SMEAR = 24.0, 12.0, 0.5
COVER, COVER_BEFORE, COVER_LONGEST = 0.35, 2.0, 4.0
ZOOM, ZOOM_SPAN, ZOOM_SAME, ZOOM_ERR = 0.4, 1.0, 0.7, 3.0
TILT, TILT_SPAN, SKY = 40.0, 1.5, 0.45


def grey(file, turned):
    w, h = SIZE
    turn = "hflip,vflip," if turned else ""
    raw = subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", "-hwaccel", "videotoolbox", "-i", str(file),  # noqa: S607
                          "-vf", f"{turn}fps={FPS},scale={w}:{h},format=gray", "-f", "rawvideo", "-"],
                         capture_output=True, check=True).stdout
    return np.frombuffer(raw, np.uint8).reshape(-1, h, w).astype(np.float32)


def motion(a, b):
    """(tx, ty, scale rate, turn, fit error) taking frame a to b."""
    h, w = a.shape
    ph, pw = h // GRID, w // GRID
    window = np.hanning(ph)[:, None] * np.hanning(pw)[None, :]
    rows, rhs = [], []
    for i in range(GRID):
        for j in range(GRID):
            pa, pb = a[i * ph:(i + 1) * ph, j * pw:(j + 1) * pw], b[i * ph:(i + 1) * ph, j * pw:(j + 1) * pw]
            dx, dy = shift(pb * window, pa * window)          # how the patch's content moved from a to b
            x, y = (j + 0.5) * pw - w / 2, (i + 0.5) * ph - h / 2
            rows += [[1, 0, x, -y], [0, 1, y, x]]
            rhs += [dx, dy]
    m, rhs = np.array(rows, float), np.array(rhs, float)
    p, *_ = np.linalg.lstsq(m, rhs, rcond=None)
    err = float(np.sqrt(np.mean((m @ p - rhs) ** 2)))
    return float(p[0]), float(p[1]), float(p[2]), float(p[3]), err


def sharpness(g):
    return float(np.mean(np.abs(4 * g[1:-1, 1:-1] - g[:-2, 1:-1] - g[2:, 1:-1] - g[1:-1, :-2] - g[1:-1, 2:])))


def signals(frames):
    moves = [motion(frames[k], frames[k + 1]) for k in range(len(frames) - 1)]
    return {"tx": [round(m[0], 2) for m in moves], "ty": [round(m[1], 2) for m in moves],
            "scale": [round(m[2], 5) for m in moves], "turn": [round(m[3], 5) for m in moves], "err": [round(m[4], 2) for m in moves],
            "sharp": [round(sharpness(g), 2) for g in frames],
            "soft": [round(float(held.soft_blocks(g[:g.shape[0] // held.BLOCK * held.BLOCK]).mean()), 3) for g in frames],
            "dark": [round(float((g < DARK).mean()), 3) for g in frames]}


def runs(flags):
    """(first, last) of each run of True."""
    out, start = [], None
    for k, f in enumerate([*flags, False]):
        if f and start is None:
            start = k
        if not f and start is not None:
            out.append((start, k - 1))
            start = None
    return out


def pan_word(tx, ty):
    """Where the camera turned, from how the picture moved (the opposite way)."""
    if abs(tx) >= abs(ty):
        return "向右" if tx < 0 else "向左"
    return "向下" if ty < 0 else "向上"


def events(s):
    tx, ty, scale = np.array(s["tx"]), np.array(s["ty"]), np.array(s["scale"])
    sharp, soft, dark = np.array(s["sharp"]), np.array(s["soft"]), np.array(s["dark"])
    speed = np.hypot(tx, ty)
    out = []
    smeared = sharp[1:] < SMEAR * max(np.median(sharp), 1e-6)
    fast = (speed >= WHIP) & np.r_[speed[1:] >= WHIP, False] | (speed >= WHIP_SMEARED) & smeared
    for a, b in runs(fast):
        k = a + int(np.argmax(speed[a:b + 1]))
        out.append({"rule": "whip", "t": (k + 1) / FPS, "from": a / FPS, "to": (b + 2) / FPS,
                    "direction": pan_word(tx[a:b + 1].sum(), ty[a:b + 1].sum()), "strength": round(float(speed[a:b + 1].max()), 1)})
    before = int(COVER_BEFORE * FPS)
    cover = np.maximum(soft, dark)
    base = np.array([np.median(cover[max(0, k - before):k]) if k else cover[0] for k in range(len(cover))])
    for a, b in runs(cover - base >= COVER):
        if (b - a + 1) / FPS <= COVER_LONGEST and b < len(cover) - 1:
            k = a + int(np.argmax(cover[a:b + 1] - base[a:b + 1]))
            out.append({"rule": "cover", "t": k / FPS, "from": a / FPS, "to": (b + 1) / FPS,
                        "direction": "变暗" if dark[k] > soft[k] else "失焦", "strength": round(float((cover - base)[k]), 2)})
    span = int(ZOOM_SPAN * FPS)
    summed = np.convolve(scale, np.ones(span), mode="same")
    err = np.array(s["err"])
    for sign, word in ((1, "推近"), (-1, "拉远")):
        for a, b in runs(sign * summed >= ZOOM):
            k = a + int(np.argmax(sign * summed[a:b + 1]))
            lo, hi = max(0, k - span // 2), min(len(scale), k + span // 2 + 1)
            if np.mean(sign * scale[lo:hi] > 0) < ZOOM_SAME or np.median(err[lo:hi]) >= ZOOM_ERR:
                continue
            out.append({"rule": "zoom", "t": (k + 1) / FPS, "from": a / FPS, "to": (b + 2) / FPS, "direction": word,
                        "strength": round(float(abs(summed[k])), 3)})
    span = int(TILT_SPAN * FPS)
    tilted = np.convolve(ty, np.ones(span), mode="valid")       # summed over the span starting at each frame
    for a, b in runs(np.abs(tilted) >= TILT):
        end = min(b + span, len(soft) - 1)
        if soft[end] >= SKY:
            out.append({"rule": "tilt", "t": end / FPS, "from": a / FPS, "to": end / FPS,
                        "direction": "仰向天空" if tilted[a:b + 1].sum() > 0 else "俯向地面", "strength": round(float(soft[end]), 2)})
    return out


def main():
    out_path = Path(sys.argv[1])
    if "--events" in sys.argv:          # the rules again on the signals already measured
        done = read_json(out_path)
        for v in done["videos"].values():
            v["events"] = events(v["signals"])
        out_path.write_text(json.dumps(done, ensure_ascii=False), encoding="utf-8")
        return
    case = sys.argv[2] if len(sys.argv) > 2 else "pj"
    d = cut.index_dir(case)
    ix = windows.Index.load(str(d / "index.json"), str(d / "frames.npz"))
    turned = {Path(v).name for v in cut.read_json(cut.CARDS / "rotation.json").get(cut.RAW[case], [])} \
        if (cut.CARDS / "rotation.json").exists() else set()
    done = read_json(out_path) if out_path.exists() else {"case": case, "fps": FPS, "videos": {}}
    for video, meta in ix.videos.items():
        if video in done["videos"]:
            continue
        file = VIDEOS / meta["file"]
        s = signals(grey(file, file.name in turned))
        ev = events(s)
        done["videos"][video] = {"file": meta["file"], "duration": meta["duration"], "turned": file.name in turned,
                                 "signals": s, "events": ev}
        out_path.write_text(json.dumps(done, ensure_ascii=False), encoding="utf-8")
        print(video, f"{meta['duration']:.0f}s", ", ".join(f"{e['rule']} {e['t']:.1f}s {e['direction']}" for e in ev) or "-", flush=True)


if __name__ == "__main__":
    main()
