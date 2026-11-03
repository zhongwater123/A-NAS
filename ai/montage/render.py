"""Stills or video clips to an H.264 MP4 with FFmpeg: 1920×1080, 30 fps, letterboxed.

Stills have no sound; clips keep the sound of the same span of their source (#350).
"""

import contextlib
import json
import os
import secrets
import subprocess
import time

WIDTH, HEIGHT, FPS = 1920, 1080, 30
CROSSFADE = 0.5
TIMEOUT = 600
RATE = 48000
# Each clip's sound fades in and out this long, so that a cut between two clips does not pop.
SOUND_FADE = 0.03
FIT = (f"scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease,"
       f"pad={WIDTH}:{HEIGHT}:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p")


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
    graph = []
    for i, image in enumerate(images):
        length = seconds + CROSSFADE if fade and i < len(images) - 1 else seconds
        args += ["-framerate", str(FPS), "-i", image]
        # Each still is decoded once and its frame repeated: an image that does
        # not decode ends the input at once, where -loop 1 would retry forever.
        graph.append(f"[{i}:v]{FIT},tpad=stop_mode=clone:stop_duration={length:g},fps={FPS},"
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
    return [*args, "-filter_complex", ";".join(graph), "-map", f"[{last}]", "-t", f"{total:g}", *_encode(output)]


def clip_args(clips: list[tuple[str, float, float]], output: str, sound: list[bool] | None = None) -> list[str]:
    """The FFmpeg command for (video, start, end) clips in order, each cut with an accurate seek.

    Every clip is brought to one size and rate before the concat filter, so
    sources of different sizes and codecs join; the total is the clips' sum.
    Each clip carries the sound of its own span, or silence where sound[i] says
    its source has none: brought to one rate and stereo, padded or cut to the
    clip's length so that picture and sound stay aligned, and faded at both ends.
    """
    sound = sound if sound is not None else [True] * len(clips)
    args = ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-n"]
    graph = []
    for i, ((video, start, end), heard) in enumerate(zip(clips, sound, strict=True)):
        length = end - start
        args += ["-ss", f"{start:.3f}", "-t", f"{length:.3f}", "-i", video]
        graph.append(f"[{i}:v]{FIT},fps={FPS},trim=duration={length:.3f},setpts=PTS-STARTPTS[v{i}]")
        # first_pts=0 keeps sound that starts late after the seek where it was, instead of pulling it forward.
        source = (f"[{i}:a]aresample={RATE}:async=1:first_pts=0," if heard
                  else f"anullsrc=r={RATE}:cl=stereo,")
        fade = min(SOUND_FADE, length / 4)
        graph.append(f"{source}aformat=sample_fmts=fltp:sample_rates={RATE}:channel_layouts=stereo,apad,"
                     f"atrim=duration={length:.3f},afade=t=in:d={fade:.3f},"
                     f"afade=t=out:st={length - fade:.3f}:d={fade:.3f},asetpts=PTS-STARTPTS[a{i}]")
    graph.append("".join(f"[v{i}][a{i}]" for i in range(len(clips))) + f"concat=n={len(clips)}:v=1:a=1[x][s]")
    return [*args, "-filter_complex", ";".join(graph), "-map", "[x]", "-map", "[s]", *_encode(output, sound=True)]


def has_sound(video: str) -> bool:
    """Whether the file has an audio stream; a file ffprobe cannot read counts as having none."""
    try:
        done = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "a", "-show_entries",  # noqa: S607  FFmpeg from PATH
                               "stream=index", "-of", "json", video],
                              capture_output=True, text=True, timeout=60, check=False)
    except FileNotFoundError:
        raise RenderError("找不到 ffprobe，请先安装") from None
    if done.returncode != 0:
        return False
    return bool(json.loads(done.stdout or "{}").get("streams"))


def render(images: list[str], seconds: float, crossfade: bool, output_dir: str) -> str:
    """Writes a new file in output_dir and returns its path; nothing is left behind on failure."""
    return _write(lambda partial: ffmpeg_args(images, seconds, crossfade, partial), output_dir, "slideshow")


def render_clips(clips: list[tuple[str, float, float]], output_dir: str) -> str:
    """Like render, for video clips."""
    heard = {video: has_sound(video) for video, _s, _e in clips}
    return _write(lambda partial: clip_args(clips, partial, [heard[video] for video, _s, _e in clips]),
                  output_dir, "montage")


def _encode(output: str, sound: bool = False) -> list[str]:
    audio = ["-c:a", "aac", "-b:a", "160k", "-ar", str(RATE), "-ac", "2"] if sound else ["-an"]
    return ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-r", str(FPS), *audio,
            "-movflags", "+faststart", "-f", "mp4", output]


def _write(command, output_dir: str, prefix: str) -> str:
    os.makedirs(output_dir, exist_ok=True)
    name = time.strftime(f"{prefix}-%Y%m%d-%H%M%S-") + secrets.token_hex(3) + ".mp4"
    final = os.path.join(output_dir, name)
    partial = final + ".partial"
    try:
        done = subprocess.run(command(partial), capture_output=True, text=True, timeout=TIMEOUT, check=False)
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
    with contextlib.suppress(FileNotFoundError):
        os.remove(path)
