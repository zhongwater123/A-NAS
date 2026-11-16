"""研究和展示用的工具（第七版网页的数据、剪辑日志页、多版对比页、旋转检查、精彩度标注），按平台配置读数据。

数据目录取自 MONTAGE_PROFILE（默认 mac-dev）的配置，ANAS_MONTAGE_* 环境变量照样能改（config.py）。
取代第六版的 paths.py 和 cut.usable_cards。
"""

import os
from pathlib import Path

from .. import config
from ..adapters.store import read_json, write_json
from ..domain.options import CutOptions

PLATFORM = config.load(os.environ.get("MONTAGE_PROFILE", "mac-dev"))
STORE = PLATFORM.store
VIDEOS, INDEXES, CARDS, CUTS = STORE.videos, STORE.indexes, STORE.cards, STORE.cuts
ROOT = Path(os.environ.get("ANAS_MONTAGE_DATA", VIDEOS.parent))      # ~/A-NAS-datasets：各种网站目录的默认位置
CASES = config.cases()

__all__ = ["CARDS", "CASES", "CUTS", "INDEXES", "ROOT", "STORE", "VIDEOS", "read_json", "rotation", "usable_cards", "write_json"]


def usable_cards(key: str, opts: CutOptions | None = None) -> list[dict]:
    """第二步能选的卡片（目录过滤后），和剪辑时一样。"""
    from ..pipeline import Montage

    return Montage(PLATFORM).usable(CASES[key], opts or CutOptions())


def rotation(key: str) -> set:
    """这组素材里旋转标记写反的原片文件名（rotation.json）。"""
    path = CARDS / "rotation.json"
    return set(read_json(path).get(CASES[key].index, [])) if path.exists() else set()
