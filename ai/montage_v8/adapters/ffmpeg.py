"""FFmpeg：取帧（FrameSource）和渲染（Renderer）。平台差异只有两处，都从配置来：

- decode：解码用的硬件加速参数，如 ["-hwaccel", "videotoolbox"]（Mac）、["-hwaccel", "rkmpp"]（RK3588，需要
  支持 Rockchip MPP 的 FFmpeg，例如 ffmpeg-rockchip）、[]（CPU）。
- encode：成片的视频编码参数，如 ["-c:v", "libx264"]、["-c:v", "h264_rkmpp", ...]。

渲染沿用第六版 montage/render.py 的做法：每个镜头单独编码成一段（一次只开一个 4K 解码器，#350 里一次开全部用掉
3.4 GiB），再拼接，最后配上歌曲选段。画面不是 16:9 的（竖拍）垫一层模糊放暗的自己。
"""

import contextlib
import json
import os
import secrets
import subprocess
import tempfile
import time
from dataclasses import dataclass, field

import numpy as np

WIDTH, HEIGHT = 1920, 1080
RATE = 48000
SOUND_FADE = 0.03
ENDING_FADE = 1.0
WIDE_SLACK = 0.05
BLUR_SIZE = (480, 270)
FIT = (f"scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease,"
       f"pad={WIDTH}:{HEIGHT}:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,format=yuv420p")
BLURRED = (f"split[back][front];[back]scale={BLUR_SIZE[0]}:{BLUR_SIZE[1]}:force_original_aspect_ratio=increase,"
           f"crop={BLUR_SIZE[0]}:{BLUR_SIZE[1]},gblur=sigma=12,scale={WIDTH}:{HEIGHT},eq=brightness=-0.08[behind];"
           f"[front]scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease[shown];"
           f"[behind][shown]overlay=(W-w)/2:(H-h)/2,setsar=1,format=yuv420p")


class FfmpegError(RuntimeError):
    pass


@dataclass
class Ffmpeg:
    """一套 FFmpeg 的用法。binary/probe：可执行文件；decode/encode 见模块说明；timeout：每条命令最多几秒。"""

    binary: str = "ffmpeg"
    probe: str = "ffprobe"
    decode: list = field(default_factory=list)
    encode: list = field(default_factory=lambda: ["-c:v", "libx264"])
    fps: int = 30
    timeout: int = 600

    def run(self, args: list, *, text: bool = False) -> subprocess.CompletedProcess:
        try:
            done = subprocess.run([self.binary, "-nostdin", "-hide_banner", "-v", "error", *args],
                                  capture_output=True, text=text, timeout=self.timeout, check=False)
        except FileNotFoundError:
            raise FfmpegError(f"找不到 {self.binary}") from None
        except subprocess.TimeoutExpired:
            raise FfmpegError(f"FFmpeg 超过 {self.timeout} 秒") from None
        if done.returncode < 0:
            raise FfmpegError(f"FFmpeg 被信号 {-done.returncode} 终止，多半是内存不够")
        if done.returncode:
            err = done.stderr if text else done.stderr.decode("utf-8", "replace")
            raise FfmpegError("FFmpeg 失败：" + ((err.strip().splitlines() or ["没有输出"])[-1]))
        return done

    # FrameSource ----------------------------------------------------------------------------------------

    def grey(self, file, start, end, size, fps, turned=False):
        w, h = size
        turn = "hflip,vflip," if turned else ""
        if end is None:
            args = ["-ss", f"{start:.3f}", "-i", str(file), "-frames:v", "1", "-vf", f"{turn}scale={w}:{h},format=gray"]
        else:
            args = [*self.decode, "-ss", f"{start:.3f}", "-t", f"{end - start:.3f}", "-i", str(file),
                    "-vf", f"{turn}fps={fps},scale={w}:{h},format=gray"]
        raw = self.run([*args, "-f", "rawvideo", "-"]).stdout
        return np.frombuffer(raw, np.uint8).reshape(-1, h, w).astype(np.float32)

    def grey_full(self, file, t, width):
        """t 秒那一帧，宽 width 的灰度（夜空锐点）。"""
        raw = self.run(["-ss", f"{t:.3f}", "-i", str(file), "-frames:v", "1", "-vf", f"scale={width}:-2,format=gray",
                        "-f", "rawvideo", "-"]).stdout
        return np.frombuffer(raw, np.uint8).reshape(-1, width).astype(np.float32)

    def still(self, file, t, out, width=512):
        os.makedirs(os.path.dirname(out), exist_ok=True)
        # 离文件结尾不到一帧时取不到（大疆的片子，结尾前 0.02 秒）：往前退。
        for at in (t, t - 0.5, t - 1.0):
            with contextlib.suppress(FfmpegError):
                self.run(["-ss", f"{max(0.0, at):.3f}", "-i", str(file), "-frames:v", "1", "-vf", f"scale={width}:-2",
                          "-q:v", "3", "-y", str(out)])
            if os.path.exists(out):
                return True
        return False

    def blur(self, image):
        out = self.run(["-i", str(image), "-vf", "blurdetect,metadata=print:file=-", "-f", "null", "-"], text=True).stdout
        return float(next(line.split("=", 1)[1] for line in out.splitlines() if "lavfi.blur=" in line))

    # Renderer -------------------------------------------------------------------------------------------

    def _probe(self, args: list) -> dict:
        try:
            done = subprocess.run([self.probe, "-v", "error", *args, "-of", "json"], capture_output=True, text=True,
                                  timeout=60, check=False)
        except FileNotFoundError:
            raise FfmpegError(f"找不到 {self.probe}") from None
        return json.loads(done.stdout or "{}") if done.returncode == 0 else {}

    def has_sound(self, video: str) -> bool:
        return bool(self._probe(["-select_streams", "a", "-show_entries", "stream=index", video]).get("streams"))

    def shown_size(self, video: str) -> tuple[int, int] | None:
        streams = self._probe(["-select_streams", "v:0", "-show_streams", video]).get("streams")
        if not streams:
            return None
        s = streams[0]
        rotation = next((int(d.get("rotation", 0)) for d in s.get("side_data_list", []) if "rotation" in d),
                        int(s.get("tags", {}).get("rotate", 0)))
        width, height = s.get("width") or 0, s.get("height") or 0
        if not width or not height:
            return None
        return (height, width) if rotation % 180 else (width, height)

    def not_wide(self, video: str) -> bool:
        size = self.shown_size(video)
        return bool(size) and abs(size[0] / size[1] / (WIDTH / HEIGHT) - 1) > WIDE_SLACK

    def part_args(self, video, start, end, heard, output, ending=0.0, slow=1.0, *, blurred=False, turned=False):
        """一个镜头编码成一段，带它自己那一段的声音（或静音），两端淡入淡出；ending 时最后几秒画面和声音淡出。"""
        length = (end - start) * slow
        heard = heard and slow == 1
        source = f"[0:a]aresample={RATE}:async=1:first_pts=0," if heard else f"anullsrc=r={RATE}:cl=stereo,"
        fade = min(SOUND_FADE, length / 4)
        out = min(ending, length / 2) if ending else fade
        picture = f",fade=t=out:st={length - out:.3f}:d={out:.3f}" if ending else ""
        turn = "hflip,vflip," if turned else ""
        stretch = f"setpts={slow:g}*PTS," if slow != 1 else ""
        graph = (f"[0:v]{turn}{BLURRED if blurred else FIT},{stretch}fps={self.fps},trim=duration={length:.3f},setpts=PTS-STARTPTS{picture}[x];"
                 f"{source}aformat=sample_fmts=fltp:sample_rates={RATE}:channel_layouts=stereo,apad,"
                 f"atrim=duration={length:.3f},afade=t=in:d={fade:.3f},"
                 f"afade=t=out:st={length - out:.3f}:d={out:.3f},asetpts=PTS-STARTPTS[s]")
        return [*self.decode, "-ss", f"{start:.3f}", "-t", f"{end - start:.3f}", "-i", video, "-filter_complex", graph,
                "-map", "[x]", "-map", "[s]", *self.encode, "-pix_fmt", "yuv420p", "-r", str(self.fps),
                "-c:a", "pcm_s16le", "-f", "matroska", "-n", output]

    def render(self, clips, song, excerpt_start, seconds, out, turned=frozenset()):
        """各镜头单独编码、拼接（画面直接复制），再配上歌曲选段（开头淡入 0.3 秒、结尾淡出 2.5 秒）。"""
        heard = {c[0]: self.has_sound(c[0]) for c in clips}
        blurred = {c[0]: self.not_wide(c[0]) for c in clips}
        os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
        with tempfile.TemporaryDirectory(dir=os.path.dirname(out) or ".") as work:
            listing = os.path.join(work, "parts.txt")
            with open(listing, "w", encoding="utf-8") as file:
                for i, (video, start, end, *slow) in enumerate(clips):
                    part = os.path.join(work, f"{i:03d}.mkv")
                    self.run(self.part_args(video, start, end, heard[video], part,
                                            ENDING_FADE if i == len(clips) - 1 else 0.0, *slow,
                                            blurred=blurred[video], turned=video in turned))
                    file.write("file '" + part.replace("'", "'\\''") + "'\n")
            joined = os.path.join(work, "joined.mp4")
            self.run(["-f", "concat", "-safe", "0", "-i", listing, "-c:v", "copy", "-an", "-movflags", "+faststart", joined])
            partial = f"{out}.{time.strftime('%H%M%S')}-{secrets.token_hex(3)}.partial.mp4"
            try:
                self.run(["-y", "-i", joined, "-ss", f"{excerpt_start:.3f}", "-t", f"{seconds:.3f}", "-i", song,
                          "-map", "0:v", "-map", "1:a", "-c:v", "copy", "-c:a", "aac", "-b:a", "192k",
                          "-af", f"afade=t=in:d=0.3,afade=t=out:st={max(0.0, seconds - 2.5):.3f}:d=2.5", "-shortest", partial])
                os.replace(partial, out)
            finally:
                with contextlib.suppress(FileNotFoundError):
                    os.remove(partial)
        return out
