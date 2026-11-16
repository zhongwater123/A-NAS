"""端口：领域需要、但随平台不同的外部能力。每个端口一个 Protocol，adapters/ 里按平台实现，profiles/ 里选。

| 端口 | 开发机（macOS） | x86 NAS | RK3588 |
|---|---|---|---|
| FrameSource | FFmpeg + VideoToolbox | FFmpeg（CPU） | FFmpeg + rkmpp |
| Describer | llama-server（Metal） | llama-server | llama-server（CPU）或 RKLLM 服务（NPU），待实测 |
| Planner | claude 命令行 | Claude API | Claude API |
| MusicAnalyzer | allin1 | 预先算好的 JSON | 预先算好的 JSON |
| Renderer | FFmpeg + libx264 | FFmpeg + libx264 | FFmpeg + h264_rkmpp |
"""

from typing import Protocol

import numpy as np


class FrameSource(Protocol):
    """从视频取帧。"""

    def grey(self, file: str, start: float, end: float | None, size: tuple[int, int], fps: float,
             turned: bool = False) -> np.ndarray:
        """灰度帧数组 (n, h, w)：end 为 None 时只要 start 那一帧，否则从 start 到 end 每秒 fps 帧。"""
        ...

    def still(self, file: str, t: float, out: str, width: int = 512) -> bool:
        """把 t 秒那一帧存成 JPEG（宽 width）；取不到返回 False。"""
        ...

    def blur(self, image: str) -> float:
        """一张图的模糊度（越大越糊）。"""
        ...


class Describer(Protocol):
    """视觉模型描述一张卡片。"""

    def describe(self, system: str, content: list, schema: dict) -> tuple[dict, float, list[str]]:
        """(回答, 秒数, 重试记录)。content：按顺序的 ("text", 文字) 和 ("image", 图片路径)；回答必须符合 schema。"""
        ...


class Planner(Protocol):
    """规划用的大模型。"""

    def ask(self, system: str, user: str, schema: dict) -> tuple[dict, dict]:
        """(回答, 开销)：回答符合 schema；开销有 seconds、model、usd、input、output。"""
        ...


class MusicAnalyzer(Protocol):
    """歌曲的段落和小节。"""

    def music(self, song: str, target: float) -> dict:
        """music.from_structure 的结果：选段、段落、镜头槽。"""
        ...


class Renderer(Protocol):
    """把时间线渲染成配乐的成片。"""

    def render(self, clips: list[tuple], song: str, excerpt_start: float, seconds: float, out: str) -> str:
        """clips：(视频文件, 起, 止[, 放慢倍数])；配上 song 从 excerpt_start 起的 seconds 秒；返回成片路径。"""
        ...

    @property
    def fps(self) -> int: ...
