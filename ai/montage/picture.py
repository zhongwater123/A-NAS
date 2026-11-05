"""How the camera moves in a card and how its picture looks, measured cheaply (#326).

A small vision model asked about camera motion answered by the order of the
choices, even on frames a second apart (docs/research/video-moment-retrieval.md);
here it is measured. Corners tracked from one frame to the next, STEP apart
(OpenCV's pyramidal Lucas-Kanade), give each step a similarity transform about
the centre of the picture: its shift is the camera turning, its scale the
camera walking on, and what is left of the shift once its moving average is
taken away is shake. The same frames give sharpness (the variance of the
Laplacian), brightness, the share of blown highlights, and how much the picture
changes from one frame to the next, whether the camera or what it films moves
(#350: a segment's pace).

OpenCV is the AI Worker's own dependency (requirements.in); FFmpeg decodes.
"""

import json
import subprocess

import cv2
import numpy as np

STEP = 0.2
WIDTH = 480
TIMEOUT = 300
# Corners tracked per step, and the fewest a step is measured on.
CORNERS = 300
CORNERS_MIN = 20
# Steps in the moving average that shake is measured against: one second.
SMOOTH = 5

# Frames are made this wide before their change is measured.
CHANGE_WIDTH = 60

# What the camera does, from the median step scaled to a second: a shift of
# PAN of the width turns, a scale of ZOOM walks on.
PAN = 0.06
TILT = 0.06
ZOOM = 0.015


def size(video: str) -> tuple[int, int]:
    """The width and height frames are decoded at: WIDTH wide, even, the video's aspect."""
    probe = subprocess.run(
        ["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height",  # noqa: S607
         "-of", "json", video], check=True, capture_output=True, timeout=TIMEOUT)
    stream = json.loads(probe.stdout)["streams"][0]
    return WIDTH, round(WIDTH * stream["height"] / stream["width"] / 2) * 2


def frames(video: str, start: float, end: float, shape: tuple[int, int], step: float = STEP) -> np.ndarray:
    """Grey frames every step from start to end, decoded with an accurate seek."""
    width, height = shape
    out = subprocess.run(
        ["ffmpeg", "-nostdin", "-v", "error", "-ss", f"{start:.3f}", "-i", video, "-t", f"{end - start:.3f}",  # noqa: S607
         "-vf", f"fps=1/{step},scale={width}:{height},format=gray", "-f", "rawvideo", "-"],
        check=True, capture_output=True, timeout=TIMEOUT)
    return np.frombuffer(out.stdout, np.uint8).reshape(-1, height, width)


def steps(grey: np.ndarray) -> np.ndarray:
    """Per step from one frame to the next: shift right and down as shares of the width, and
    log scale; NaN where too few corners were tracked."""
    width = grey.shape[2]
    centre = np.array([grey.shape[2] / 2, grey.shape[1] / 2], np.float32)
    out = np.full((max(len(grey) - 1, 0), 3), np.nan)
    for k in range(len(grey) - 1):
        corners = cv2.goodFeaturesToTrack(grey[k], CORNERS, 0.01, 8)
        if corners is None or len(corners) < CORNERS_MIN:
            continue
        moved, ok, _err = cv2.calcOpticalFlowPyrLK(grey[k], grey[k + 1], corners, None)
        ok = ok.ravel() == 1
        if ok.sum() < CORNERS_MIN:
            continue
        transform, _inliers = cv2.estimateAffinePartial2D(corners[ok] - centre, moved[ok] - centre, method=cv2.RANSAC)
        if transform is None:
            continue
        out[k] = (transform[0, 2] / width, transform[1, 2] / width, np.log(np.hypot(transform[0, 0], transform[1, 0])))
    return out


def motion(pan: float, tilt: float, zoom: float) -> str:
    """The camera's motion in words, from its median shift and scale per second."""
    if abs(pan) >= PAN and abs(pan) >= abs(tilt):
        return "向左转" if pan > 0 else "向右转"
    if abs(tilt) >= TILT:
        return "抬头" if tilt > 0 else "低头"
    if abs(zoom) >= ZOOM:
        return "向前走" if zoom > 0 else "向后退"
    return "固定"


def measure(grey: np.ndarray, step: float = STEP) -> dict:
    """The camera's motion and the picture's looks over frames step apart."""
    moves = steps(grey)
    known = moves[~np.isnan(moves).any(axis=1)]
    pan, tilt, zoom = (np.median(known, axis=0) / step).tolist() if len(known) else (0.0, 0.0, 0.0)
    shake = 0.0
    if len(known) > SMOOTH:
        kernel = np.ones(SMOOTH) / SMOOTH
        residual = [known[:, k] - np.convolve(known[:, k], kernel, mode="same") for k in (0, 1)]
        inner = slice(SMOOTH // 2, len(known) - SMOOTH // 2)
        shake = float(np.sqrt(np.mean(np.square(np.stack(residual)[:, inner]))))
    sharp = [cv2.Laplacian(frame, cv2.CV_64F).var() for frame in grey]
    return {
        "motion": motion(pan, tilt, zoom),
        "pan": round(pan, 4), "tilt": round(tilt, 4), "zoom": round(zoom, 4), "shake": round(shake, 5),
        "sharp": round(float(np.median(sharp)), 1) if sharp else 0.0,
        "bright": round(float(np.median([frame.mean() for frame in grey])), 1) if len(grey) else 0.0,
        "clipped": round(float(np.median([(frame >= 250).mean() for frame in grey])), 4) if len(grey) else 0.0,
        "change": change(grey),
    }


def change(grey: np.ndarray) -> float:
    """How much the picture changes per step: the median over steps of the mean absolute
    difference of one frame from the next, as a share of full scale, on frames made
    CHANGE_WIDTH wide first so that film grain does not count (case-08's still sniper
    0.016 at 480 wide, 0.007 at 60)."""
    if len(grey) < 2:
        return 0.0
    height = max(1, round(CHANGE_WIDTH * grey.shape[1] / grey.shape[2]))
    small = np.stack([cv2.resize(frame, (CHANGE_WIDTH, height), interpolation=cv2.INTER_AREA) for frame in grey])
    steps_ = np.abs(np.diff(small.astype(np.int16), axis=0)).mean(axis=(1, 2)) / 255
    return round(float(np.median(steps_)), 4)
