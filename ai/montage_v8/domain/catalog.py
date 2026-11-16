"""规划能用的卡片目录：去掉误拍、画质差的、（只用横拍时）竖拍的卡片；人标的精彩度怎么读。"""

from .quality import poor

MARK = {"3": "精彩", "2": "可用", "1": "平淡", "x": "有问题"}


def left_out(excluded: list[dict]) -> dict:
    """用户标的误拍（手指挡镜头、黑场）：{视频: [(起, 止)]}。excluded 是 excluded.json 里这组素材的条目。"""
    out = {}
    for x in excluded:
        out.setdefault(x["video"], []).append((float(x["from"]), float(x["to"])))
    return out


def usable(described: list[dict], measures: dict, kept: set, excluded: list[dict], *, quality: bool = True,
           wide: bool = False, upright=None) -> list[dict]:
    """能进目录的卡片，按原顺序。measures：quality-<case>.json；kept：人工复核保留的卡片；upright(card)：竖拍。"""
    bad = poor(measures, kept) if quality else {}
    out_spans = left_out(excluded)
    return [c for c in described if c["id"] not in bad
            and not any(a - 0.01 <= c["start"] < b - 0.01 for a, b in out_spans.get(c["video"], []))
            and not (wide and upright(c))]


def card_mark(labels: dict, cid: str, beats: int) -> str:
    """第一步看到的卡片精彩度：卡片里最好的节拍；精彩带个数，全部有问题才算有问题。"""
    marks = [labels.get(f"{cid}/{n}") for n in range(1, beats + 1)]
    if "3" in marks:
        return f"精彩（{marks.count('3')}）"
    for v in ("2", "1"):
        if v in marks:
            return MARK[v]
    return MARK["x"] if marks and all(m == "x" for m in marks) else ""


def best_videos(labels: dict, cards: dict) -> set:
    """有节拍被标为精彩的视频。"""
    return {cards[b.rsplit("/", 1)[0]]["video"] for b, mark in labels.items()
            if mark == "3" and b.rsplit("/", 1)[0] in cards}
