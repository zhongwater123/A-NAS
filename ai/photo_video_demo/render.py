"""Stills to an H.264 MP4 with FFmpeg: 1920×1080, 30 fps, letterboxed, no audio."""

import os
import secrets
import subprocess
import time

WIDTH, HEIGHT, FPS = 1920, 1080, 30
CROSSFADE = 0.5
TIMEOUT = 600


class RenderError(Exception):
    pass


def ffmpeg_args(images: list[str], seconds: float, crossfade: bool, output: str) -> list[str]:
    """The FFmpeg command for the stills in order.

    The total is always len(images) × seconds. With a crossfade each still but
    the last stays CROSSFADE longer, and xfade overlaps that much with the next,
    so the k-th transition starts at (k+1) × seconds.
    """
    fade = crossfade and len(images) > 1
    args = ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-n"]
    fit = (f"scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease,"
           f"pad={WIDTH}:{HEIGHT}:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p")
    graph = []
    for i, image in enumerate(images):
        length = seconds + CROSSFADE if fade and i < len(images) - 1 else seconds
        args += ["-framerate", str(FPS), "-i", image]
        # Each still is decoded once and its frame repeated: an image that does
        # not decode ends the input at once, where -loop 1 would retry forever.
        graph.append(f"[{i}:v]{fit},tpad=stop_mode=clone:stop_duration={length:g},fps={FPS},"
                     f"trim=duration={length:g}[v{i}]")
    if fade:
        last = "v0"
        for k in range(1, len(images)):
            graph.append(f"[{last}][v{k}]xfade=transition=fade:duration={CROSSFADE:g}:offset={k * seconds:g}[x{k}]")
            last = f"x{k}"
    else:
        graph.append("".join(f"[v{i}]" for i in range(len(images))) + f"concat=n={len(images)}:v=1:a=0[x]")
        last = "x"
    total = len(images) * seconds
    return args + ["-filter_complex", ";".join(graph), "-map", f"[{last}]", "-t", f"{total:g}",
                   "-c:v", "libx264", "-pix_fmt", "yuv420p", "-r", str(FPS), "-an",
                   "-movflags", "+faststart", "-f", "mp4", output]


def render(images: list[str], seconds: float, crossfade: bool, output_dir: str) -> str:
    """Writes a new file in output_dir and returns its path; nothing is left behind on failure."""
    os.makedirs(output_dir, exist_ok=True)
    name = time.strftime("slideshow-%Y%m%d-%H%M%S-") + secrets.token_hex(3) + ".mp4"
    final = os.path.join(output_dir, name)
    partial = final + ".partial"
    try:
        done = subprocess.run(ffmpeg_args(images, seconds, crossfade, partial),
                              capture_output=True, text=True, timeout=TIMEOUT, check=False)
    except FileNotFoundError:
        raise RenderError("找不到 ffmpeg，请先安装") from None
    except subprocess.TimeoutExpired:
        _remove(partial)
        raise RenderError(f"拼接超过 {TIMEOUT} 秒，已放弃") from None
    if done.returncode != 0:
        _remove(partial)
        raise RenderError("ffmpeg 失败：" + (done.stderr.strip().splitlines() or ["没有输出"])[-1])
    try:
        # A link never replaces an existing file, unlike a rename.
        os.link(partial, final)
    finally:
        _remove(partial)
    return final


def _remove(path: str) -> None:
    try:
        os.remove(path)
    except FileNotFoundError:
        pass
