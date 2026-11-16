"""文件存储：素材、窗口索引、卡片和剪辑结果都是数据目录里的 JSON 和图片，格式和第六版相同。

videos   原片，每组素材一个文件夹
indexes  每组素材的窗口索引 video-moments-<索引名>/embeddinggemma*/{index.json,frames.npz}（A-NAS 视频索引器建）
cards    cards-<键>.json、quality-<键>.json、excluded.json、reviewed.json、rotation.json、frames/
cuts     音乐、规划、成片：music-*.json、pick-*.json、shots-*.json、cut-*.json、held-*.json、*.mp4
"""

import json
import os
from dataclasses import dataclass
from pathlib import Path

import numpy as np

from ..domain.cards import WindowIndex
from ..domain.picture import GRID


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def write_json(obj, path):
    """先写临时文件再改名：中途停掉不会留下半个文件。"""
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    partial = path.with_name(path.name + ".partial")
    partial.write_text(json.dumps(obj, ensure_ascii=False, indent=1), encoding="utf-8")
    os.replace(partial, path)


@dataclass
class Store:
    videos: Path
    indexes: Path
    cards: Path
    cuts: Path

    def __post_init__(self):
        self.videos, self.indexes, self.cards, self.cuts = (Path(os.path.expanduser(str(p))) for p in
                                                            (self.videos, self.indexes, self.cards, self.cuts))

    # 窗口索引与原片 -----------------------------------------------------------------------------------

    def index_dir(self, index: str) -> Path:
        return next((self.indexes / f"video-moments-{index}").glob("embeddinggemma*"))

    def windows(self, index: str) -> WindowIndex:
        d = self.index_dir(index)
        meta = read_json(d / "index.json")
        with np.load(d / "frames.npz") as data:
            vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
        return WindowIndex.build(meta, vectors)

    @staticmethod
    def read_vectors(path) -> dict:
        path = Path(path)
        if not path.exists():
            return {}
        with np.load(path) as data:
            return dict(zip(data["keys"].tolist(), data["vectors"], strict=True))

    @staticmethod
    def write_vectors(vectors: dict, path) -> None:
        """和 A-NAS 视频索引器的 frames.npz 同一个格式：keys 和 vectors 两个数组。"""
        path = Path(path)
        path.parent.mkdir(parents=True, exist_ok=True)
        keys = sorted(vectors)
        partial = path.with_name(path.name + ".partial.npz")
        np.savez(partial, keys=np.array(keys), vectors=np.stack([np.asarray(vectors[k], dtype=np.float32) for k in keys]))
        os.replace(partial, path)

    def source(self, ix: WindowIndex, video: str) -> str:
        return str(self.videos / ix.videos[video]["file"])

    def turned(self) -> set:
        """旋转标记写反的原片（rotation.json），渲染和取帧时翻回来。"""
        path = self.cards / "rotation.json"
        if not path.exists():
            return set()
        names = {n for k, v in read_json(path).items() if k != "about" for n in v}
        return {str(f) for n in names for f in self.videos.glob(f"*/{n}")}

    # 卡片 -----------------------------------------------------------------------------------------------

    def cards_file(self, key: str) -> Path:
        return self.cards / f"cards-{key}.json"

    def all_cards(self, key: str) -> dict:
        path = self.cards_file(key)
        return read_json(path) if path.exists() else {}

    def described(self, key: str) -> list[dict]:
        return [c for c in self.all_cards(key).values() if "answer" in c]

    def quality(self, key: str) -> dict:
        path = self.cards / f"quality-{key}.json"
        return read_json(path) if path.exists() else {}

    def quality_file(self, key: str) -> Path:
        return self.cards / f"quality-{key}.json"

    def excluded(self, index: str) -> list:
        path = self.cards / "excluded.json"
        return read_json(path).get(index, []) if path.exists() else []

    def reviewed(self, index: str) -> set:
        path = self.cards / "reviewed.json"
        return set(read_json(path).get(index, [])) if path.exists() else set()

    def frame_path(self, key: str, video: str, t: float) -> Path:
        return self.cards / "frames" / key / video / f"{round(t * 1000):07d}.jpg"

    @staticmethod
    def upright(card: dict) -> bool:
        """关键帧（已按播放方向）高大于宽。"""
        from PIL import Image

        width, height = Image.open(card["frames"][0]).size
        return height > width

    @staticmethod
    def grey_image(path: str, size: tuple[int, int] = GRID) -> np.ndarray:
        from PIL import Image

        return np.asarray(Image.open(path).convert("L").resize(size), dtype=np.float32)

    @staticmethod
    def mean_light(path: str) -> float:
        from PIL import Image

        return float(np.asarray(Image.open(path).convert("L"), dtype=np.float32).mean())

    # 剪辑结果 --------------------------------------------------------------------------------------------

    def cut_file(self, kind: str, key: str, tag: str) -> Path:
        return self.cuts / f"{kind}-{key}{tag}.json"


class HeldCache:
    """每个节拍可用区间的缓存（held-<键>.json，和第六版 held.Spans 同一个文件、同样的键）。"""

    def __init__(self, path: Path, compute):
        self.path, self.compute = Path(path), compute
        self.done = read_json(self.path) if self.path.exists() else {}

    def __call__(self, beat, file, key, lo, hi, turned=False):
        name = f"{beat}@{lo:.2f}-{hi:.2f}"
        if name not in self.done:
            self.done[name] = self.compute(file, key, lo, hi, turned)
            self.path.write_text(json.dumps(self.done, ensure_ascii=False, indent=0), encoding="utf-8")
        return tuple(self.done[name])
