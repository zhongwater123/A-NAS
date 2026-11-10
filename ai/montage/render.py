"""Stills or video clips to an H.264 MP4 with FFmpeg: 1920×1080, 30 fps, letterboxed.

Stills have no sound; clips keep the sound of the same span of their source (#350).
"""

import contextlib
import json
import os
import secrets
import subprocess
import tempfile
import time

WIDTH, HEIGHT, FPS = 1920, 1080, 30
CROSSFADE = 0.5
TIMEOUT = 600
RATE = 48000
# Each clip's sound fades in and out this long, so that a cut between two clips does not pop.
SOUND_FADE = 0.03
# A montage's last clip fades to black, picture and sound, this long (#350): it ends, not stops.
ENDING_FADE = 1.0
FIT = (f"scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease,"
       f"pad={WIDTH}:{HEIGHT}:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p")
VIDEO = ["-c:v", "libx264", "-pix_fmt", "yuv420p", "-r", str(FPS)]
# Sources whose rotation tag is wrong (a DJI Pocket file tagged -180 over upright pixels): turned back after
# FFmpeg turns them by the tag. (Read with -noautorotate, the part kept the tag and played upside down.)
TURNED_BACK: set[str] = set()


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


def part_args(video: str, start: float, end: float, heard: bool, output: str, ending: float = 0.0,
              slow: float = 1.0) -> list[str]:
    """The FFmpeg command for one (video, start, end) clip, cut with an accurate seek, as a part to join.

    The picture is brought to one size and rate, so that parts from sources of
    different sizes and codecs join, and encoded as the final cut is. The clip
    carries the sound of its own span, or silence where heard says its source has
    none: brought to one rate and stereo, padded or cut to the clip's length so
    that picture and sound stay aligned, faded at both ends, and kept as PCM, so
    that joining adds no encoder delay between parts. With ending, picture and sound fade
    out over that many last seconds (at most half the clip). Slowed (slow > 1), the clip plays that many
    times as long and carries silence (a music cut's ending shot drawn out to fill its slot).
    """
    length = (end - start) * slow
    heard = heard and slow == 1
    # first_pts=0 keeps sound that starts late after the seek where it was, instead of pulling it forward.
    source = f"[0:a]aresample={RATE}:async=1:first_pts=0," if heard else f"anullsrc=r={RATE}:cl=stereo,"
    fade = min(SOUND_FADE, length / 4)
    out = min(ending, length / 2) if ending else fade
    picture = f",fade=t=out:st={length - out:.3f}:d={out:.3f}" if ending else ""
    turn = "hflip,vflip," if video in TURNED_BACK else ""
    stretch = f"setpts={slow:g}*PTS," if slow != 1 else ""
    graph = (f"[0:v]{turn}{FIT},{stretch}fps={FPS},trim=duration={length:.3f},setpts=PTS-STARTPTS{picture}[x];"
             f"{source}aformat=sample_fmts=fltp:sample_rates={RATE}:channel_layouts=stereo,apad,"
             f"atrim=duration={length:.3f},afade=t=in:d={fade:.3f},"
             f"afade=t=out:st={length - out:.3f}:d={out:.3f},asetpts=PTS-STARTPTS[s]")
    return ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-n",
            "-ss", f"{start:.3f}", "-t", f"{end - start:.3f}", "-i", video, "-filter_complex", graph,
            "-map", "[x]", "-map", "[s]", *VIDEO, "-c:a", "pcm_s16le", "-f", "matroska", output]


def join_args(listing: str, output: str) -> list[str]:
    """The FFmpeg command that joins the parts listed (concat demuxer): the picture copied, the sound encoded once."""
    return ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-n", "-f", "concat", "-safe", "0",
            "-i", listing, "-c:v", "copy", "-c:a", "aac", "-b:a", "160k", "-ar", str(RATE), "-ac", "2",
            "-movflags", "+faststart", "-f", "mp4", output]


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
    return _write(lambda partial: _run(ffmpeg_args(images, seconds, crossfade, partial)), output_dir, "slideshow")


def render_clips(clips: list[tuple], output_dir: str, ending: bool = False) -> str:
    """Like render, for video clips (video, start, end), or (video, start, end, slow) for a slowed one: each is
    encoded on its own, then the parts are joined. With ending, the last clip fades to black (ENDING_FADE).

    One clip at a time keeps one decoder in memory: a 4K source opened once per
    clip in a single command held a decoder for every clip and passed 3.4 GiB
    (#350, twelve clips of a 4K film).
    """
    heard = {clip[0]: has_sound(clip[0]) for clip in clips}

    def make(partial):
        with tempfile.TemporaryDirectory(dir=output_dir) as work:
            listing = os.path.join(work, "parts.txt")
            with open(listing, "w", encoding="utf-8") as file:
                for i, (video, start, end, *slow) in enumerate(clips):
                    part = os.path.join(work, f"{i:03d}.mkv")
                    _run(part_args(video, start, end, heard[video], part,
                                   ENDING_FADE if ending and i == len(clips) - 1 else 0.0, *slow))
                    file.write("file '" + part.replace("'", "'\\''") + "'\n")
            _run(join_args(listing, partial))

    return _write(make, output_dir, "montage")


def _encode(output: str) -> list[str]:
    return [*VIDEO, "-an", "-movflags", "+faststart", "-f", "mp4", output]


def _run(command: list[str]) -> None:
    try:
        done = subprocess.run(command, capture_output=True, text=True, timeout=TIMEOUT, check=False)
    except FileNotFoundError:
        raise RenderError("找不到 ffmpeg，请先安装") from None
    except subprocess.TimeoutExpired:
        raise RenderError(f"拼接超过 {TIMEOUT} 秒，已放弃") from None
    if done.returncode < 0:
        raise RenderError(f"ffmpeg 被信号 {-done.returncode} 终止，多半是内存不够")
    if done.returncode != 0:
        raise RenderError("ffmpeg 失败：" + (done.stderr.strip().splitlines() or ["没有输出"])[-1])


def _write(make, output_dir: str, prefix: str) -> str:
    """Has make write the file at the path it is given, then gives it its final name."""
    os.makedirs(output_dir, exist_ok=True)
    name = time.strftime(f"{prefix}-%Y%m%d-%H%M%S-") + secrets.token_hex(3) + ".mp4"
    final = os.path.join(output_dir, name)
    partial = final + ".partial"
    try:
        make(partial)
        # A link never replaces an existing file, unlike a rename.
        os.link(partial, final)
    finally:
        _remove(partial)
    return final


def _remove(path: str) -> None:
    with contextlib.suppress(FileNotFoundError):
        os.remove(path)
