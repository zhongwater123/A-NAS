"""平台配置：profiles/<名字>.toml 决定数据目录和每个端口用哪个适配器，cases.toml 是各组素材的设定。

数据目录也可以用环境变量改（和第六版 paths.py 相同）：ANAS_MONTAGE_VIDEOS、ANAS_MONTAGE_INDEXES、
ANAS_MONTAGE_CARDS、ANAS_MONTAGE_CUTS。
"""

import os
import tomllib
from dataclasses import dataclass
from pathlib import Path

from .adapters.ffmpeg import Ffmpeg
from .adapters.music import Allin1, Precomputed
from .adapters.planner import ClaudeApi, ClaudeCli
from .adapters.store import Store
from .adapters.vision import OpenAIVision
from .domain.options import Case

PROFILES = Path(__file__).resolve().parent / "profiles"
ENV = {"videos": "ANAS_MONTAGE_VIDEOS", "indexes": "ANAS_MONTAGE_INDEXES", "cards": "ANAS_MONTAGE_CARDS",
       "cuts": "ANAS_MONTAGE_CUTS"}


class ConfigError(ValueError):
    pass


@dataclass
class Platform:
    name: str
    store: Store
    ffmpeg: Ffmpeg
    describer: object
    planner: object
    music: object
    workers: int


def _pick(kinds: dict, section: dict, what: str):
    kind = section.get("kind")
    if kind not in kinds:
        raise ConfigError(f"[{what}] kind 必须是 {'、'.join(kinds)} 之一，现在是 {kind!r}")
    args = {k: v for k, v in section.items() if k != "kind"}
    return kinds[kind](**args)


def load(name: str) -> Platform:
    path = Path(name) if name.endswith(".toml") else PROFILES / f"{name}.toml"
    if not path.exists():
        raise ConfigError(f"没有配置 {path}；可用的：{', '.join(p.stem for p in PROFILES.glob('*.toml') if p.stem != 'cases')}")
    raw = tomllib.loads(path.read_text(encoding="utf-8"))
    paths = {k: os.environ.get(ENV[k]) or raw["paths"][k] for k in ENV}
    store = Store(**paths)
    music = dict(raw.get("music", {"kind": "precomputed"}))
    music.setdefault("folder", str(store.cuts))
    planner = dict(raw["planner"])
    if planner.get("kind") == "claude-cli":
        planner.setdefault("cwd", str(store.cuts))
    return Platform(
        name=path.stem, store=store, ffmpeg=Ffmpeg(**raw.get("ffmpeg", {})),
        describer=_pick({"openai": OpenAIVision}, raw["describer"], "describer"),
        planner=_pick({"claude-cli": ClaudeCli, "claude-api": ClaudeApi}, planner, "planner"),
        music=_pick({"allin1": Allin1, "precomputed": Precomputed}, music, "music"),
        workers=int(raw.get("workers", 1)))


def cases() -> dict[str, Case]:
    raw = tomllib.loads((PROFILES / "cases.toml").read_text(encoding="utf-8"))
    return {key: Case(key=key, **value) for key, value in raw.items()}
