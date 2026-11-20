"""画面分析：对已经解码好的灰度帧做计算，不解码视频（解码由 FrameSource 端口按平台做）。

来自第六版 held.py（镜头可用区间）和 quality.py（抖动、遮挡、夜空锐点）的计算部分。
"""

from collections.abc import Callable

import numpy as np

# held.py：镜头里有东西从下面挡进来（一个人、口袋、门框），它离镜头近、是失焦的一片软块。
GRID, BLOCK, SOFT = (256, 144), 16, 6.0
STEP = 0.5          # 和关键帧比较的帧之间隔几秒
NEW_MOST = 0.08     # 关键帧上没有、从底边伸上来的软块占画面这么多就算挡住
# quality.py
SHAKE_SIZE, SMOOTH = (320, 180), 5
FILLED, BLACK = 0.6, 35
POINT_RISE = 20


def soft_blocks(g: np.ndarray) -> np.ndarray:
    """灰度帧按 BLOCK 见方分块，拉普拉斯的 95 分位低于 SOFT 的块是软的（用分位数而不是均值，星空仍算锐）。"""
    h, w = g.shape
    lap = np.pad(np.abs(4 * g[1:-1, 1:-1] - g[:-2, 1:-1] - g[2:, 1:-1] - g[1:-1, :-2] - g[1:-1, 2:]), 1)
    rows, cols = h // BLOCK, w // BLOCK
    tiles = lap.reshape(rows, BLOCK, cols, BLOCK).transpose(0, 2, 1, 3).reshape(rows, cols, -1)
    return np.percentile(tiles, 95, axis=2) < SOFT


def patches(mask: np.ndarray) -> list[list[tuple[int, int]]]:
    """块掩码的 4 连通片，每片是 (行, 列) 的列表。"""
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


def new_patch(frame: np.ndarray, key_soft: np.ndarray) -> float:
    """关键帧上不软、碰到底边的最大软片占画面的比例。"""
    mask = soft_blocks(frame) & ~key_soft
    bottom = mask.shape[0] - 1
    return max((len(p) for p in patches(mask) if max(y for y, _ in p) == bottom), default=0) / mask.size


# frames(start, end) -> 灰度帧数组：end 为 None 时只要 start 那一帧，否则从 start 到 end 每 STEP 秒一帧。
Frames = Callable[[float, float | None], np.ndarray]


def clear(frames: Frames, key: float, lo: float, hi: float) -> tuple[float, float]:
    """关键帧两侧、lo..hi 之内，到第一张被挡住的帧为止（取它和前一帧的中点）的区间。"""
    ref = soft_blocks(frames(key, None)[0])
    a, b = lo, hi
    after = frames(key, hi) if hi - key > STEP / 2 else []
    for i, f in enumerate(after):
        if new_patch(f, ref) >= NEW_MOST:
            b = max(key, key + i * STEP - STEP / 2)
            break
    before = frames(lo, key) if key - lo > STEP / 2 else []
    for i in range(len(before) - 1, -1, -1):
        if new_patch(before[i], ref) >= NEW_MOST:
            a = min(key, lo + i * STEP + STEP / 2)
            break
    return round(a, 3), round(b, 3)


def shift(a: np.ndarray, b: np.ndarray) -> tuple[float, float]:
    """把灰度帧 a 移到 b 的 (dx, dy)：相位相关，抛物线插值到亚像素。"""
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


def jitter(f: np.ndarray) -> float:
    """一段 SHAKE_SIZE 灰度帧的抖动中位数：每帧的位移减去前后 SMOOTH 帧的平均（平稳的摇镜不算抖）。"""
    h, w = f.shape[1:]
    if len(f) < SMOOTH + 2:
        return 0.0
    window = np.hanning(h)[:, None] * np.hanning(w)[None, :]
    moves = np.array([shift(f[i] * window, f[i + 1] * window) for i in range(len(f) - 1)])
    kernel = np.ones(SMOOTH) / SMOOTH
    smooth = np.stack([np.convolve(moves[:, k], kernel, mode="same") for k in range(2)], axis=1)
    return round(float(np.median(np.hypot(*(moves - smooth)[SMOOTH // 2:-(SMOOTH // 2)].T))), 3)


def blocked(g: np.ndarray) -> float:
    """GRID 大小的灰度关键帧上，从底边伸上来、填满自身外框 FILLED 以上、不是近黑的软片占画面的比例。"""
    h, w = g.shape
    soft = soft_blocks(g)
    light = g.reshape(h // BLOCK, BLOCK, w // BLOCK, BLOCK).mean(axis=(1, 3))
    best = 0.0
    for patch in patches(soft):
        ys, xs = [y for y, _ in patch], [x for _, x in patch]
        fill = len(patch) / ((max(ys) - min(ys) + 1) * (max(xs) - min(xs) + 1))
        dark = float(np.mean([light[y, x] for y, x in patch])) < BLACK
        if max(ys) == soft.shape[0] - 1 and fill >= FILLED and not dark:
            best = max(best, len(patch) / soft.size)
    return round(best, 2)


def box(a: np.ndarray, r: int) -> np.ndarray:
    """每个 (2r+1) 见方的均值，边缘取最近值。"""
    p = np.pad(a, r + 1, mode="edge").cumsum(0).cumsum(1)
    n = 2 * r + 1
    s = p[n:, n:] - p[:-n, n:] - p[n:, :-n] + p[:-n, :-n]
    return s[: a.shape[0], : a.shape[1]] / (n * n)


def sharp_points(g: np.ndarray) -> float:
    """全分辨率灰度帧上每百万像素的锐点数：比周围 5×5 均值亮 POINT_RISE 以上的像素。"""
    return round(float(((g - box(g, 2)) > POINT_RISE).sum()) / g.size * 1e6, 1)
