"""第六版 (2026-11-09): how each card's picture holds up, so that blurred, shaking or blocked cards can be
left out before planning. The 4B's issues field said 无明显问题 for all 341 DJI cards, the finger over the
lens in 新西兰 018 included, so each is measured on its own:

  blur    FFmpeg's blurdetect on each key frame (512 wide), the card's median; higher is blurrier.
  shake   the frame-to-frame shift of 320x180 grey frames at SHAKE_FPS (phase correlation, numpy's FFT:
          there is no OpenCV here and FFmpeg's vidstab is not built in), less its own smoothed path, so a
          steady pan is not shake: the median jitter in pixels of a 320-wide frame.
  blocked by rule, on each key frame (asked of the 4B, it caught 3 of 14 frames of 018's finger and pocket):
          something at the lens is out of focus, so it is a solid soft patch. The frame in 16x9 blocks,
          a block soft when the 95th percentile of its Laplacian is under SOFT (the percentile, not the
          mean, keeps a starry sky sharp); the largest 4-connected soft patch that reaches the bottom row
          (a sky sits above the scene and never does; a finger, a pocket, a door frame comes up from below)
          and fills at least FILLED of its bounding box (a patch, not an outline), and is not near-black (a
          night or sunset skyline is black at the bottom, an aurora sky all over; a lens covered black is
          blur's, whose blurdetect finds nothing to measure): its share of the frame. The card's value is
          the median over its key frames.

  python3 quality.py ne ch     (writes quality-<case>.json beside the cards; resumes)
"""

import json
import subprocess
import sys
import time
from pathlib import Path

import numpy as np
from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))  # ai/, for montage
import cut
from paths import read_json, write_json

SHAKE_FPS, SHAKE_SIZE, SMOOTH = 10, (320, 180), 5
GRID, BLOCK = (256, 144), 16
SOFT, FILLED, BLACK = 6.0, 0.6, 35


def blur(frame):
    """FFmpeg's blurdetect of one image."""
    out = subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", "-i", frame, "-vf", "blurdetect,metadata=print:file=-",  # noqa: S607
                          "-f", "null", "-"], capture_output=True, text=True, check=True).stdout
    return float(next(line.split("=", 1)[1] for line in out.splitlines() if "lavfi.blur=" in line))


def shift(a, b):
    """The (dx, dy) that moves grey frame a onto b, by phase correlation with a parabolic sub-pixel peak."""
    fa, fb = np.fft.fft2(a), np.fft.fft2(b)
    r = fa * np.conj(fb)
    c = np.fft.ifft2(r / (np.abs(r) + 1e-9)).real
    y, x = np.unravel_index(np.argmax(c), c.shape)

    def sub(v, n, line):
        left, mid, right = line[(v - 1) % n], line[v], line[(v + 1) % n]
        den = left - 2 * mid + right
        off = 0.5 * (left - right) / den if abs(den) > 1e-9 else 0.0
        p = v + off
        return p - n if p > n / 2 else p

    return sub(x, c.shape[1], c[y, :]), sub(y, c.shape[0], c[:, x])


def shake(file, start, end):
    """The card's median jitter: each frame's shift less the mean of its SMOOTH neighbours."""
    w, h = SHAKE_SIZE
    raw = subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", "-hwaccel", "videotoolbox", "-ss", f"{start:.3f}",  # noqa: S607
                          "-t", f"{end - start:.3f}", "-i", str(file), "-vf", f"fps={SHAKE_FPS},scale={w}:{h},format=gray",
                          "-f", "rawvideo", "-"], capture_output=True, check=True).stdout
    f = np.frombuffer(raw, np.uint8).reshape(-1, h, w).astype(np.float32)
    if len(f) < SMOOTH + 2:
        return 0.0
    window = np.hanning(h)[:, None] * np.hanning(w)[None, :]
    moves = np.array([shift(f[i] * window, f[i + 1] * window) for i in range(len(f) - 1)])
    kernel = np.ones(SMOOTH) / SMOOTH
    smooth = np.stack([np.convolve(moves[:, k], kernel, mode="same") for k in range(2)], axis=1)
    jitter = np.hypot(*(moves - smooth)[SMOOTH // 2:-(SMOOTH // 2)].T)
    return round(float(np.median(jitter)), 3)


def blocked(frame):
    """The share of the frame a soft patch from the bottom covers (see the module's doc); 0 for none."""
    w, h = GRID
    g = np.asarray(Image.open(frame).convert("L").resize((w, h)), dtype=np.float32)
    lap = np.pad(np.abs(4 * g[1:-1, 1:-1] - g[:-2, 1:-1] - g[2:, 1:-1] - g[1:-1, :-2] - g[1:-1, 2:]), 1)
    rows, cols = h // BLOCK, w // BLOCK
    tiles = lap.reshape(rows, BLOCK, cols, BLOCK).transpose(0, 2, 1, 3).reshape(rows, cols, -1)
    soft = np.percentile(tiles, 95, axis=2) < SOFT
    light = g.reshape(rows, BLOCK, cols, BLOCK).mean(axis=(1, 3))
    seen, best = np.zeros_like(soft), 0.0
    for r0 in range(rows):
        for c0 in range(cols):
            if not soft[r0, c0] or seen[r0, c0]:
                continue
            seen[r0, c0], todo, patch = True, [(r0, c0)], []
            while todo:
                y, x = todo.pop()
                patch.append((y, x))
                for yy, xx in ((y + 1, x), (y - 1, x), (y, x + 1), (y, x - 1)):
                    if 0 <= yy < rows and 0 <= xx < cols and soft[yy, xx] and not seen[yy, xx]:
                        seen[yy, xx] = True
                        todo.append((yy, xx))
            ys, xs = [y for y, _ in patch], [x for _, x in patch]
            share = len(patch) / soft.size
            fill = len(patch) / ((max(ys) - min(ys) + 1) * (max(xs) - min(xs) + 1))
            dark = float(np.mean([light[y, x] for y, x in patch])) < BLACK
            if max(ys) == rows - 1 and fill >= FILLED and not dark:
                best = max(best, share)
    return round(best, 2)


RULE_AGAIN = "--rule" in sys.argv     # the blocked rule again on measured cards (it is cheap)


def main():
    cases = [a for a in sys.argv[1:] if not a.startswith("--")]
    for case in cases:
        key = cut.CASES[case][0]
        d = next((Path.home() / "A-NAS-datasets" / "cache" / f"video-moments-{key}").glob("embeddinggemma*"))
        videos = json.loads((d / "index.json").read_text(encoding="utf-8"))["videos"]
        path = cut.cards_dir(case) / f"quality-{case}.json"
        done = read_json(path) if path.exists() else {}
        t0 = time.time()
        for n, c in enumerate(cut.load(case), 1):
            if c["id"] in done and "blockedRule" in done[c["id"]] and not RULE_AGAIN:
                continue
            if c["id"] in done:       # measured before the rule: the 4B's answers dropped, the rule's added
                e = done[c["id"]]
                e.pop("blocked", None)
                e.pop("blockedFrames", None)
                marks = [blocked(f) for f in c["frames"]]
                e |= {"blocked": float(np.median(marks)), "blockedRule": marks}
                write_json(done, path)
                continue
            blurs = [blur(f) for f in c["frames"]]
            done[c["id"]] = {"blur": round(float(np.median(blurs)), 3), "blurs": [round(b, 3) for b in blurs],
                             "shake": shake(cut.VIDEOS / videos[c["video"]]["file"], c["start"], c["end"])}
            marks = [blocked(f) for f in c["frames"]]
            done[c["id"]] |= {"blocked": float(np.median(marks)), "blockedRule": marks}
            write_json(done, path)
            e = done[c["id"]]
            print(case, n, c["id"], f"{time.time() - t0:.0f}s", "模糊", e["blur"], "抖动", e["shake"], "遮挡", e["blocked"], flush=True)
        print(case, "done", flush=True)


if __name__ == "__main__":
    main()
