"""Where the working files live: outside the repository, in a data folder. Each can be moved by its
environment variable; the defaults are the development machine's (2026-11).

  VIDEOS   the source videos, one folder per set (ANAS_MONTAGE_VIDEOS)
  INDEXES  each set's window index, video-moments-<key>/embeddinggemma*/{index.json,frames.npz}, built
           beforehand by A-NAS's video indexer (ANAS_MONTAGE_INDEXES)
  CARDS    cards, key frames, quality measures and the user's marks: cards-<case>.json, quality-<case>.json,
           excluded.json, rotation.json, reviewed.json, frames/ (ANAS_MONTAGE_CARDS)
  CUTS     songs' structures and slots, plans, cuts and the rendered films (ANAS_MONTAGE_CUTS)
"""
import json
import os
from pathlib import Path

ROOT = Path.home() / "A-NAS-datasets"
VIDEOS = Path(os.environ.get("ANAS_MONTAGE_VIDEOS", ROOT / "videos"))
INDEXES = Path(os.environ.get("ANAS_MONTAGE_INDEXES", ROOT / "cache"))
CARDS = Path(os.environ.get("ANAS_MONTAGE_CARDS", ROOT / "剪辑对比" / "第五版"))
CUTS = Path(os.environ.get("ANAS_MONTAGE_CUTS", ROOT / "剪辑对比" / "第四版" / "剪法24（2026-11-08）"))


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def write_json(obj, path):
    Path(path).write_text(json.dumps(obj, ensure_ascii=False, indent=1), encoding="utf-8")
