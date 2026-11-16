"""Score photos with a release bundle: image -> frozen tower -> two heads.

The weights are checked against ``MANIFEST.json`` before they are loaded.
On CPU the tower runs in fp32, on CUDA in bf16 as during training and eval;
image preprocessing (the base model's processor and pixel budget) is the
one the head was trained with. Phone photos carry their rotation in EXIF, so
images are turned upright first. Calls are serialized with a lock, so one
scorer can serve several threads.
"""

from __future__ import annotations

import io
import threading
import time
from pathlib import Path

import torch
from PIL import Image, ImageOps, UnidentifiedImageError
from pydantic import Field

from qv_head.head import load_head
from qv_head.release import (
    HEAD_PATH,
    MODEL_FILES,
    TOWER_PATH,
    ReleaseError,
    ReleaseManifest,
    load_manifest,
    verify_files,
)
from qv_head.schema import Record
from qv_head.tower import open_tower, sync

# What Pillow decodes without extra packages; HEIC / RAW need converting first.
IMAGE_SUFFIXES = frozenset({".jpg", ".jpeg", ".png", ".webp", ".bmp", ".tif", ".tiff"})


class ScorerError(ValueError):
    """Raised for an image that cannot be scored."""


class ScoreResult(Record):
    """Two scores on the 1-5 scale (not clipped) and what was scored."""

    aesthetic: float = Field(allow_inf_nan=False)
    technical: float = Field(allow_inf_nan=False)
    width: int = Field(gt=0)
    height: int = Field(gt=0)
    latency_ms: float = Field(gt=0)


def decode_image(data: bytes) -> Image.Image:
    """Decode bytes into an upright RGB image (EXIF rotation applied)."""

    if not data:
        raise ScorerError("图片内容为空")
    try:
        with Image.open(io.BytesIO(data)) as opened:
            opened.load()
            upright = ImageOps.exif_transpose(opened)
            return upright.convert("RGB")
    except (UnidentifiedImageError, OSError, Image.DecompressionBombError) as exc:
        raise ScorerError(f"无法解码图片: {exc}") from exc


class Scorer:
    """A verified bundle loaded on one device."""

    def __init__(self, root: Path, *, device: str = "cpu") -> None:
        manifest = load_manifest(root)
        verify_files(root, manifest, MODEL_FILES)
        precision = "bf16" if device.startswith("cuda") else "fp32"
        settings = manifest.tower.model_copy(update={"precision": precision})
        head, config, head_tower = load_head(root / HEAD_PATH, manifest.head_sha256)
        if head_tower != manifest.tower_sha256:
            raise ReleaseError("头训练时用的视觉塔与发布目录里的不是同一份")
        if config.tower != manifest.tower:
            raise ReleaseError("头训练时的特征设置与 MANIFEST.json 不符")
        if manifest.entry(f"{TOWER_PATH}/model.safetensors").sha256 != (
            manifest.tower_sha256
        ):
            raise ReleaseError("MANIFEST.json 里视觉塔的哈希前后不一致")
        self.manifest: ReleaseManifest = manifest
        self.device = device
        self.tower = open_tower(root / TOWER_PATH, settings, device)
        self.head = head.to(device).eval()
        self._lock = threading.Lock()

    @property
    def tag(self) -> str:
        """``name@version`` of the loaded bundle."""

        return self.manifest.tag

    def score(self, image: Image.Image) -> ScoreResult:
        """Score one upright RGB image."""

        with self._lock:
            encoded = self.tower.encode(image)
            sync(self.device)
            start = time.perf_counter()
            with torch.inference_mode():
                features = self.tower(encoded).unsqueeze(0)
                scores = self.head.to_scores(self.head(features))[0].cpu()
            sync(self.device)
            latency_ms = (time.perf_counter() - start) * 1000
        return ScoreResult(
            aesthetic=float(scores[0]),
            technical=float(scores[1]),
            width=image.width,
            height=image.height,
            latency_ms=latency_ms,
        )

    def score_bytes(self, data: bytes) -> ScoreResult:
        """Decode and score an encoded image (JPEG, PNG, WebP, ...)."""

        return self.score(decode_image(data))

    def score_file(self, path: Path) -> ScoreResult:
        """Read, decode and score an image file."""

        if not path.is_file():
            raise ScorerError(f"文件不存在: {path}")
        return self.score_bytes(path.read_bytes())
