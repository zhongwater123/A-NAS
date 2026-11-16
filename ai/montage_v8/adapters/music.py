"""音乐分析（MusicAnalyzer）。

- Precomputed：读预先算好的 music-<歌名>.json（选段和槽）。曲库是有限的，板子上不现算：allin1 要 PyTorch 和
  音源分离，在 RK3588 上太重。
- Allin1：开发机上现算。先用 allin1 得到段落和重拍（structure-<歌名>.json，可复用），再用响度判断铺垫和副歌，
  交给 domain.music.from_structure。
"""

import json
import os
import subprocess
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from ..domain.music import from_structure

SR = 22050
FRAME, HOP = 2048, 512      # 和 librosa.feature.rms 的默认值相同


@dataclass
class Precomputed:
    folder: str

    def music(self, song, target):
        path = Path(self.folder) / f"music-{Path(song).stem}.json"
        if not path.exists():
            raise FileNotFoundError(f"{path} 不存在：先在开发机上用 allin1 分析这首歌，随曲库一起发布")
        return json.loads(path.read_text(encoding="utf-8"))


def samples(song: str, ffmpeg: str = "ffmpeg") -> np.ndarray:
    raw = subprocess.run([ffmpeg, "-v", "error", "-i", str(song), "-ac", "1", "-ar", str(SR), "-f", "f32le", "-"],
                         capture_output=True, check=True).stdout
    return np.frombuffer(raw, np.float32)


def rms_curve(y: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """librosa.feature.rms 的默认算法（居中、两端补零）和 librosa.times_like：不依赖 librosa。"""
    padded = np.pad(y, FRAME // 2)
    n = 1 + (len(padded) - FRAME) // HOP
    frames = np.lib.stride_tricks.as_strided(padded, (n, FRAME), (padded.strides[0] * HOP, padded.strides[0]))
    rms = np.sqrt(np.mean(np.abs(frames) ** 2, axis=1))
    return rms, np.arange(n) * HOP / SR


@dataclass
class Allin1:
    """folder：structure-*.json 和 music-*.json 放在哪里；model：allin1 的模型（harmonix-fold0 只跑一折，快八倍）。"""

    folder: str
    model: str = "harmonix-all"

    def structure(self, song: str) -> dict:
        path = Path(self.folder) / f"structure-{Path(song).stem}.json"
        if path.exists():
            return json.loads(path.read_text(encoding="utf-8"))
        import tempfile

        import allin1  # 只有开发机装

        with tempfile.TemporaryDirectory() as work:
            r = allin1.analyze(str(song), out_dir=None, demix_dir=f"{work}/demix", spec_dir=f"{work}/spec",
                               device="cpu", keep_byproducts=False, model=self.model)
        out = {"song": str(song), "model": self.model, "bpm": r.bpm, "beats": [round(float(t), 3) for t in r.beats],
               "downbeats": [round(float(t), 3) for t in r.downbeats],
               "segments": [{"start": round(float(s.start), 2), "end": round(float(s.end), 2), "label": s.label}
                            for s in r.segments]}
        path.write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
        return out

    def music(self, song, target):
        st = self.structure(song)
        rms, times = rms_curve(samples(st["song"]))
        out = from_structure(st, rms, times, target)
        os.makedirs(self.folder, exist_ok=True)
        (Path(self.folder) / f"music-{Path(song).stem}.json").write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
        return out
