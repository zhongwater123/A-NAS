"""第六版 (2026-11-10): how long a beat's picture holds. A card is described at a few key frames (新西兰
ne-097 at 1 s and 13 s), and what passes between them is never seen: in fortnight-7 a man in black walked
in front of the lake at 1.4 s, and the 2.5 s shot cut from its first key frame ended on his back.

Something passing at the lens is out of focus, so it is a soft patch; the frames every STEP seconds around
the key frame are held against it, and a frame is blocked when the soft patch that was not soft at the key
frame and reaches the bottom row (a person, a pocket, a door frame comes up from below; a pan up to the sky
opens soft blocks at the top) covers NEW_MOST of it or more. Of the 53 shots of fortnight-7 and forever-8
only ne-097 had one (0.14 and 0.15, then 0.08); colour histograms against the key frame, tried first, also
moved as far for a change of exposure (ne-006) or an aerial sweep (ne-106).
"""

import json
import subprocess
from pathlib import Path

import numpy as np

GRID, BLOCK, SOFT = (256, 144), 16, 6.0
STEP = 0.5          # seconds between the frames held against the key frame
NEW_MOST = 0.08     # a new soft patch from the bottom this share of the frame or more: blocked


def frames(file, start, end=None, turned=False):
    """Grey GRID-sized frames: the one at start, or every STEP from start to end (FFmpeg, hardware decoding)."""
    w, h = GRID
    turn = "hflip,vflip," if turned else ""
    if end is None:
        args = ["-ss", f"{start:.3f}", "-i", str(file), "-frames:v", "1", "-vf", f"{turn}scale={w}:{h},format=gray"]
    else:
        args = ["-hwaccel", "videotoolbox", "-ss", f"{start:.3f}", "-t", f"{end - start:.3f}", "-i", str(file),
                "-vf", f"{turn}fps={1 / STEP},scale={w}:{h},format=gray"]
    raw = subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", *args, "-f", "rawvideo", "-"],  # noqa: S607
                         capture_output=True, check=True).stdout
    return np.frombuffer(raw, np.uint8).reshape(-1, h, w).astype(np.float32)


def soft_blocks(g):
    """The BLOCK-square blocks of a grey frame whose Laplacian's 95th percentile is under SOFT (the percentile,
    not the mean, keeps a starry sky sharp)."""
    h, w = g.shape
    lap = np.pad(np.abs(4 * g[1:-1, 1:-1] - g[:-2, 1:-1] - g[2:, 1:-1] - g[1:-1, :-2] - g[1:-1, 2:]), 1)
    rows, cols = h // BLOCK, w // BLOCK
    tiles = lap.reshape(rows, BLOCK, cols, BLOCK).transpose(0, 2, 1, 3).reshape(rows, cols, -1)
    return np.percentile(tiles, 95, axis=2) < SOFT


def patches(mask):
    """The 4-connected patches of a block mask, each a list of (row, col)."""
    rows, cols = mask.shape
    seen, out = np.zeros_like(mask), []
    for r0 in range(rows):
        for c0 in range(cols):
            if not mask[r0, c0] or seen[r0, c0]:
                continue
            seen[r0, c0], todo, patch = True, [(r0, c0)], []
            while todo:
                y, x = todo.pop()
                patch.append((y, x))
                for yy, xx in ((y + 1, x), (y - 1, x), (y, x + 1), (y, x - 1)):
                    if 0 <= yy < rows and 0 <= xx < cols and mask[yy, xx] and not seen[yy, xx]:
                        seen[yy, xx] = True
                        todo.append((yy, xx))
            out.append(patch)
    return out


def new_patch(frame, key_soft):
    """The share of the frame the largest soft patch not soft at the key frame, reaching the bottom row, covers."""
    mask = soft_blocks(frame) & ~key_soft
    bottom = mask.shape[0] - 1
    return max((len(p) for p in patches(mask) if max(y for y, _ in p) == bottom), default=0) / mask.size


def clear(file, key, lo, hi, turned=False):
    """The span around key, within lo..hi, before the first blocked frame either side (to halfway between it
    and the frame before)."""
    ref = soft_blocks(frames(file, key, turned=turned)[0])
    a, b = lo, hi
    after = frames(file, key, hi, turned) if hi - key > STEP / 2 else []
    for i, f in enumerate(after):
        if new_patch(f, ref) >= NEW_MOST:
            b = max(key, key + i * STEP - STEP / 2)
            break
    before = frames(file, lo, key, turned) if key - lo > STEP / 2 else []
    for i in range(len(before) - 1, -1, -1):
        if new_patch(before[i], ref) >= NEW_MOST:
            a = min(key, lo + i * STEP + STEP / 2)
            break
    return round(a, 3), round(b, 3)


class Spans:
    """clear() of each beat, kept in a JSON file beside the cut's other files (each costs a decode of the 4K
    source around the beat)."""

    def __init__(self, path: Path):
        self.path = path
        self.done = json.loads(path.read_text(encoding="utf-8")) if path.exists() else {}

    def get(self, beat, file, key, lo, hi, turned=False):
        name = f"{beat}@{lo:.2f}-{hi:.2f}"
        if name not in self.done:
            self.done[name] = clear(file, key, lo, hi, turned)
            self.path.write_text(json.dumps(self.done, ensure_ascii=False, indent=0), encoding="utf-8")
        return tuple(self.done[name])
