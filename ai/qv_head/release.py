"""Build the customer bundle: tower + chosen head + runtime code, all hashed.

The bundle is a plain folder that runs on its own (``score.py`` at its root,
``Dockerfile`` next to it). ``MANIFEST.json`` records where the weights come
from, the eval numbers of the chosen head, and the SHA-256 and size of every
other file, so the scorer refuses to start on a changed or truncated weight.

Every link is checked before anything is written: the selection names the
head, the head names the tower it was trained on, and the eval run must have
scored exactly that head on exactly that tower.
"""

from __future__ import annotations

import os
import shutil
import tarfile
from datetime import datetime
from pathlib import Path
from typing import Annotated, Literal

from pydantic import AwareDatetime, Field, model_validator

from qv_head.config import TowerSettings
from qv_head.data import sha256_file
from qv_head.head import load_head
from qv_head.records import (
    EvaEvalReport,
    HeadEvalRunConfig,
    SweepSelection,
    TrainSummary,
)
from qv_head.schema import (
    EVA_DIMENSIONS,
    SCORE_MAX,
    SCORE_MIN,
    DimensionMetrics,
    Record,
    RunName,
    SchemaVersion,
    Sha256,
    TowerFile,
)
from qv_head.training import SUMMARY_FILE
from qv_head.weights import (
    CONFIG_FILE,
    MODEL_ID,
    PREPROCESSOR_FILE,
    REVISION,
    TOWER_DIR,
    TOWER_WEIGHTS_FILE,
    verify_tower,
)

MANIFEST_FILE = "MANIFEST.json"
TOWER_PATH = "model/tower"
HEAD_PATH = "model/head.pt"
TOWER_FILES = (CONFIG_FILE, TOWER_WEIGHTS_FILE, PREPROCESSOR_FILE)
# The scorer checks these before loading; ``score.py verify`` checks all.
MODEL_FILES = tuple(f"{TOWER_PATH}/{name}" for name in TOWER_FILES) + (HEAD_PATH,)
# Repository file -> bundle path, besides the weights and qv_head/*.py.
RUNTIME_FILES = {
    "release/score.py": "score.py",
    "release/requirements.txt": "requirements.txt",
    "release/Dockerfile": "Dockerfile",
    "release/docker-compose.yml": "docker-compose.yml",
    "release/dockerignore": ".dockerignore",
    "release/README.md": "README.md",
}
LICENSE_PATH = "licenses/Qwen3.5-4B-LICENSE.txt"
BASE_LICENSE = "Apache-2.0"

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


def _chosen(runs_dir: Path, prefix: str) -> tuple[SweepSelection, TrainSummary]:
    path = runs_dir / f"{prefix}-selection.json"
    if not path.is_file():
        raise ReleaseError(f"找不到选模记录 {path}")
    selection = SweepSelection.model_validate_json(path.read_text(encoding="utf-8"))
    summary_path = runs_dir / selection.chosen_run_id / SUMMARY_FILE
    summary = TrainSummary.model_validate_json(summary_path.read_text(encoding="utf-8"))
    if summary.checkpoint != selection.checkpoint or summary.limited:
        raise ReleaseError("选中的检查点与训练记录不一致，或是冒烟运行")
    return selection, summary


def _eval_of(
    runs_dir: Path, eval_run_id: str, summary: TrainSummary
) -> tuple[HeadEvalRunConfig, EvaEvalReport]:
    run_dir = runs_dir / eval_run_id
    config = HeadEvalRunConfig.model_validate_json(
        (run_dir / "config.json").read_text(encoding="utf-8")
    )
    report = EvaEvalReport.model_validate_json(
        (run_dir / "report.json").read_text(encoding="utf-8")
    )
    if config.split != "eval" or report.split != "eval":
        raise ReleaseError(f"{eval_run_id} 不是 eval 划分上的评测")
    if (
        config.train_run_id != summary.run_id
        or config.checkpoint != summary.checkpoint
        or config.tower_sha256 != summary.tower_sha256
    ):
        raise ReleaseError(f"{eval_run_id} 评的不是选中的头或不是同一份视觉塔")
    if report.metric_errors or set(report.metrics) != set(EVA_DIMENSIONS):
        raise ReleaseError(f"{eval_run_id} 的指标不完整")
    return config, report


def _copy(source: Path, bundle: Path, path: str) -> TowerFile:
    if not source.is_file():
        raise ReleaseError(f"缺少 {source}")
    target = bundle / path
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    return TowerFile(
        path=path, sha256=sha256_file(target), size_bytes=target.stat().st_size
    )


def build_release(
    *,
    code_root: Path,
    models_dir: Path,
    runs_dir: Path,
    prefix: str,
    eval_run_id: str,
    license_file: Path,
    out_dir: Path,
    name: str,
    version: str,
    git_commit: str,
    git_dirty: bool,
    now: datetime,
) -> ReleaseManifest:
    """Write the bundle folder; checks every input first, never overwrites."""

    partial = out_dir.with_name(out_dir.name + ".partial")
    for path in (out_dir, partial):
        if path.exists():
            raise ReleaseError(f"{path} 已存在，不覆盖")
    weights = verify_tower(models_dir)
    if (weights.model_id, weights.revision) != (MODEL_ID, REVISION):
        raise ReleaseError("视觉塔不是从固定版本的 Qwen3.5-4B 切出来的")
    tower_sha = weights.find(TOWER_WEIGHTS_FILE).sha256
    selection, summary = _chosen(runs_dir, prefix)
    head_path = runs_dir / selection.checkpoint.path
    _, config, head_tower = load_head(head_path, selection.checkpoint.sha256)
    if not tower_sha == head_tower == summary.tower_sha256:
        raise ReleaseError("头训练时用的视觉塔与 models/ 里的不是同一份")
    eval_config, report = _eval_of(runs_dir, eval_run_id, summary)

    partial.mkdir(parents=True)
    files = [
        _copy(models_dir / TOWER_DIR / item, partial, f"{TOWER_PATH}/{item}")
        for item in TOWER_FILES
    ]
    files.append(_copy(head_path, partial, HEAD_PATH))
    files += [
        _copy(source, partial, f"qv_head/{source.name}")
        for source in sorted((code_root / "qv_head").glob("*.py"))
    ]
    files += [
        _copy(code_root / source, partial, target)
        for source, target in RUNTIME_FILES.items()
    ]
    files.append(_copy(license_file, partial, LICENSE_PATH))
    manifest = ReleaseManifest(
        schema_version="1.0",
        name=name,
        version=version,
        created_at=now,
        base_model=BaseModelRef(
            model_id=MODEL_ID, revision=REVISION, license=BASE_LICENSE
        ),
        tower=config.tower,
        dimensions=EVA_DIMENSIONS,
        score_min=SCORE_MIN,
        score_max=SCORE_MAX,
        train_run_id=summary.run_id,
        best_epoch=summary.best.epoch,
        tower_sha256=tower_sha,
        head_sha256=selection.checkpoint.sha256,
        eval=EvalSummary(
            dataset="eva",
            run_id=report.run_id,
            n_items=report.n_items,
            ids_fingerprint=report.ids_fingerprint,
            metrics=report.metrics,
            device=eval_config.device,
            latency_p50_ms=report.latency_p50_ms,
        ),
        git_commit=git_commit,
        git_dirty=git_dirty,
        files=tuple(sorted(files, key=lambda f: f.path)),
    )
    (partial / MANIFEST_FILE).write_text(
        manifest.model_dump_json(indent=2) + "\n", encoding="utf-8", newline="\n"
    )
    os.replace(partial, out_dir)
    return manifest


def write_release_tar(bundle: Path) -> tuple[Path, str]:
    """Write ``<bundle>.tar`` (one top folder) and its ``.sha256``; no overwrite."""

    manifest = load_manifest(bundle)
    verify_files(bundle, manifest)
    output = bundle.with_name(bundle.name + ".tar")
    checksum = bundle.with_name(bundle.name + ".tar.sha256")
    for path in (output, checksum):
        if path.exists():
            raise ReleaseError(f"{path} 已存在，不覆盖")
    names = [MANIFEST_FILE] + [item.path for item in manifest.files]
    with tarfile.open(output, "w") as archive:
        for name in names:
            archive.add(bundle / name, arcname=f"{bundle.name}/{name}")
    digest = sha256_file(output)
    checksum.write_text(f"{digest}  {output.name}\n", encoding="utf-8", newline="\n")
    return output, digest
