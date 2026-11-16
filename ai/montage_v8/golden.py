"""基线比对：同一份描述、同一份两步规划，第八版填槽的镜头表要和第六版（eval/montage_v5/cut.py 当前代码）完全相同。

第六版在这里跑到渲染之前为止：渲染和写文件被替换掉，只拿它算出的 clips。两边读同一个 held-<组>.json 缓存，
缓存里没有的区间会去解码原片（两边各算一次，结果相同）。只在开发机上有数据时能跑。
"""

import importlib
import os
import sys
import tempfile
from pathlib import Path

from .adapters.store import read_json
from .domain.options import CutOptions

V6 = Path(__file__).resolve().parents[1] / "eval" / "montage_v5"
FIELDS = ("card", "beats", "start", "end", "video", "part", "picture", "slot", "share", "slow", "beat")


def v6_clips(case_key: str, tag: str, music: str, cards_dir: str, cuts_dir: str, wide: bool, labels: str) -> list[dict]:
    os.environ |= {"CUT_TAG": tag, "CUT_MUSIC": music, "CUT_WIDE": "1" if wide else "", "CUT_LABELS": labels,
                   "ANAS_MONTAGE_CARDS": cards_dir, "ANAS_MONTAGE_CUTS": cuts_dir}
    sys.path.insert(0, str(V6))
    sys.path.insert(0, str(V6.parents[1]))
    for name in ("paths", "cut", "held", "windows"):
        sys.modules.pop(name, None)
    cut = importlib.import_module("cut")
    captured = {}
    with tempfile.NamedTemporaryFile(suffix=".mp4") as dummy:
        cut.render.render_clips = lambda *_a, **_k: dummy.name

        class Done:
            returncode = 0

        cut.subprocess.run = lambda *_a, **_k: Done()
        cut.Path.unlink = lambda *_a, **_k: None
        cut.write_json = lambda obj, path: captured.setdefault(Path(path).name, obj)
        cut.cut_to_music(case_key)
    return captured[f"cut-{case_key}{tag}.json"]["clips"]


def compare(montage, case, tag: str, music: str) -> bool:
    store = montage.store
    picked = read_json(store.cut_file("pick", case.key, tag))
    labels_file = picked.get("labels", "")
    opts = CutOptions(tag=tag, wide=bool(picked.get("wide")), labels=read_json(labels_file)["labels"] if labels_file else {},
                      labels_file=labels_file)
    music_file = f"music-{Path(music).stem.removeprefix('music-')}.json"
    _m, mine, _short = montage.timeline(case, opts, music_file)
    theirs = v6_clips(case.key, tag, music_file, str(store.cards), str(store.cuts), opts.wide, labels_file)
    ok = len(mine) == len(theirs)
    for n, (a, b) in enumerate(zip(mine, theirs, strict=False), 1):
        diff = [f for f in FIELDS if (round(a[f], 6) if isinstance(a.get(f), float) else a.get(f))
                != (round(b[f], 6) if isinstance(b.get(f), float) else b.get(f))]
        if diff:
            ok = False
            print(f"镜头 {n} 不同：", {f: (a.get(f), b.get(f)) for f in diff})
    print(f"{case.key}{tag}：第八版 {len(mine)} 个镜头，第六版 {len(theirs)} 个，" + ("完全相同" if ok else "有差异"))
    return ok
