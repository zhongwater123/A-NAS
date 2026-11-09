"""Where 剪法25's working files live: outside the repository, in the data folder the cards, plans
and cuts were made in (2026-11-08/09). ANAS_MONTAGE_DATA moves it."""
import json
import os
from pathlib import Path

DATA = Path(os.environ.get("ANAS_MONTAGE_DATA", Path.home() / "A-NAS-datasets" / "剪辑对比" / "第四版"))
# Cards (cards.py, process_cards.py): key frames, beats, process, summary per card.
CARDS = DATA / "关键帧卡片（2026-11-08）"
# Plans, shots, cuts and the rendered films (cut.py), and the 报幕 marks.
CUTS = DATA / "剪法24（2026-11-08）"
# 方案 B's prompt profiles (film, travel-vlog) and tag_split.py, whose helpers tag_beats.py uses.
B_DIR = DATA / "模型对比-2B-4B" / "b"
VIDEOS = Path.home() / "A-NAS-datasets" / "videos"


def read_json(path):
    return json.loads(Path(path).read_text(encoding="utf-8"))


def write_json(obj, path):
    Path(path).write_text(json.dumps(obj, ensure_ascii=False, indent=1), encoding="utf-8")
