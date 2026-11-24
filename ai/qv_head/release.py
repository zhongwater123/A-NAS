"""Read and validate the pinned scoring bundle (the inference subset of #480)."""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Annotated, Literal

from pydantic import AwareDatetime, Field, model_validator

from qv_head.config import TowerSettings
from qv_head.schema import EVA_DIMENSIONS, DimensionMetrics, Record, RunName, SchemaVersion, Sha256, TowerFile

MANIFEST_FILE = "MANIFEST.json"
HEAD_PATH = "model/head.pt"
TOWER_PATH = "model/tower"
MODEL_FILES = (HEAD_PATH, f"{TOWER_PATH}/config.json", f"{TOWER_PATH}/preprocessor_config.json",
               f"{TOWER_PATH}/model.safetensors")


def sha256_file(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


Version = Annotated[str, Field(pattern=r"^\d+\.\d+\.\d+$")]
GitCommit = Annotated[str, Field(pattern=r"^[0-9a-f]{40}$")]


class ReleaseError(ValueError):
    """Raised for inconsistent inputs, a bad bundle or an existing output."""


class BaseModelRef(Record):
    """The base model the tower is cut from, and its licence."""

    model_id: str = Field(min_length=1)
    revision: Annotated[str, Field(pattern=r"^[0-9a-f]{40}$")]
    license: str = Field(min_length=1)


class EvalSummary(Record):
    """The chosen head's one-time eval on the fixed EVA eval split."""

    dataset: Literal["eva"]
    run_id: RunName
    n_items: int = Field(ge=1)
    ids_fingerprint: Sha256
    metrics: dict[str, DimensionMetrics]
    device: str = Field(min_length=1)
    latency_p50_ms: float = Field(gt=0)


class ReleaseManifest(Record):
    """Everything the bundle holds and where its weights come from."""

    schema_version: SchemaVersion
    name: RunName
    version: Version
    created_at: AwareDatetime
    base_model: BaseModelRef
    tower: TowerSettings
    dimensions: tuple[str, ...]
    score_min: float
    score_max: float
    train_run_id: RunName
    best_epoch: int = Field(ge=1)
    tower_sha256: Sha256
    head_sha256: Sha256
    eval: EvalSummary
    git_commit: GitCommit
    git_dirty: bool
    files: tuple[TowerFile, ...] = Field(min_length=1)

    @model_validator(mode="after")
    def _check(self) -> ReleaseManifest:
        paths = [f.path for f in self.files]
        if len(set(paths)) != len(paths):
            raise ValueError("files 里有重复路径")
        missing = [p for p in MODEL_FILES if p not in paths]
        if missing:
            raise ValueError(f"files 缺少模型文件: {missing}")
        if self.dimensions != EVA_DIMENSIONS:
            raise ValueError(f"dimensions 必须是 {EVA_DIMENSIONS}")
        if set(self.eval.metrics) != set(EVA_DIMENSIONS):
            raise ValueError("eval.metrics 必须正好有两个分数")
        return self

    @property
    def tag(self) -> str:
        """``name@version``, written next to every score."""

        return f"{self.name}@{self.version}"

    def entry(self, path: str) -> TowerFile:
        """The manifest entry of one bundle file."""

        for item in self.files:
            if item.path == path:
                return item
        raise ReleaseError(f"MANIFEST.json 里没有 {path}")


def load_manifest(root: Path) -> ReleaseManifest:
    """Read ``MANIFEST.json`` of a bundle."""

    path = root / MANIFEST_FILE
    if not path.is_file():
        raise ReleaseError(f"找不到 {path}，这不是打分器的发布目录")
    return ReleaseManifest.model_validate_json(path.read_text(encoding="utf-8"))


def verify_files(
    root: Path, manifest: ReleaseManifest, paths: tuple[str, ...] | None = None
) -> int:
    """Check size and SHA-256 of ``paths`` (all files by default)."""

    entries = (
        manifest.files
        if paths is None
        else tuple(manifest.entry(path) for path in paths)
    )
    for item in entries:
        path = root / item.path
        if not path.is_file():
            raise ReleaseError(f"缺少文件: {path}")
        if path.stat().st_size != item.size_bytes:
            raise ReleaseError(f"文件大小与 MANIFEST.json 不符（可能没传完）: {path}")
        if sha256_file(path) != item.sha256:
            raise ReleaseError(f"文件哈希与 MANIFEST.json 不符: {path}")
    return len(entries)
