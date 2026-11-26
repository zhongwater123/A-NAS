"""Golden comparison of the montage compute Worker with the eighth version (development machine only).

The prototype's results come from real footage and stay out of the repository
(docs/architecture/montage-center.md, 剪辑计算 Worker, 样本). Point
ANAS_VIDEOCUT_SAMPLES at the folder that holds cache/ and 剪辑对比/ (on
development machine C, ~/A-NAS-datasets) and run, in the AI runtime:

    python -m eval.videocut_golden

For each group it runs the Worker's fill the way the montage center will,
one missing clear span at a time from the prototype's held-<group>.json, and
compares the timeline with the prototype's cut-<group>-recut-1.json clip by
clip; and it cuts the cards again from the prototype's window vectors and
compares their bounds and keyframes with cards-<group>.json. It also checks
quality from the prototype's sources and keyframes and reports input hashes.
The saved prototype results must match in all three comparisons.
"""

import hashlib
import json
import os
import subprocess
import sys
from pathlib import Path

import numpy as np

from anas_videocut import worker
from anas_videocut.algo import (
    cards as cardrules,
    catalog as catalogrules,
    fill as fillrules,
    picture,
    quality as qualityrules,
)

CASES = {"pj": "pinjie", "ne": "newzealand", "ch": "chuanxi"}
CUTS = "剪辑对比/规划与成片"
CARDS = "剪辑对比/卡片"
FPS = 30


def load(path):
    return json.loads(path.read_text(encoding="utf-8"))


def window_folder(root, index):
    folders = sorted((root / "cache" / f"video-moments-{index}").glob("*/index.json"))
    if len(folders) != 1:
        raise ValueError(f"{index}: expected one prototype window index, found {len(folders)}")
    return folders[0].parent


def window_index(root, index):
    folder = window_folder(root, index)
    meta = load(folder / "index.json")
    with np.load(folder / "frames.npz") as data:
        vectors = dict(zip(data["keys"].tolist(), data["vectors"], strict=True))
    rows, frames, flat = [], [], []
    for video, entry in sorted(meta["videos"].items()):
        for window in entry["windows"]:
            rows.append({"video": video, "start": window["start"], "end": window["end"]})
            frames.append(vectors[f"{video}/{window['frame']}"])
            flat.append(window["flat"])
    return meta, rows, np.stack(frames).astype(np.float32), np.array(flat, np.uint8)


def same(a, b):
    if isinstance(a, float) or isinstance(b, float):
        return abs(float(a) - float(b)) < 1e-6
    if isinstance(a, list) and isinstance(b, list):
        return len(a) == len(b) and all(same(x, y) for x, y in zip(a, b, strict=True))
    return a == b


def fill_case(root, key, index):
    cuts, cards_dir = root / CUTS, root / CARDS
    _, rows, vectors, _ = window_index(root, index)
    described = [c for c in load(cards_dir / f"cards-{key}.json").values() if "answer" in c]
    quality = load(cards_dir / f"quality-{key}.json") if (cards_dir / f"quality-{key}.json").exists() else {}
    reviewed = set(load(cards_dir / "reviewed.json").get(index, []))
    excluded = load(cards_dir / "excluded.json").get(index, [])
    catalog = [c["id"] for c in catalogrules.usable(described, quality, reviewed, excluded)]
    cut = load(cuts / f"cut-{key}-recut-1.json")
    planned = load(cuts / f"shots-{key}-recut-1.json")
    picked = load(cuts / f"pick-{key}-recut-1.json")["answer"]
    music = load(cuts / cut["music"])
    held = load(cuts / f"held-{key}.json")
    request = {"fps": FPS, "rows": rows, "cards": {c["id"]: c for c in described}, "catalog": catalog, "music": music,
               "planned": planned, "picked": picked, "labels": planned.get("labels") or {}, "known": {}}
    calls, unmeasured = 0, []
    while True:
        calls += 1
        answer = worker.op_fill(request, {"vectors": vectors})
        if "need" not in answer:
            break
        need = answer["need"]
        if need["name"] not in held:
            # The prototype's cache comes from the run that made the cut; a
            # span it never measured is taken as clear, in both runs below.
            unmeasured.append(need["name"])
        request["known"][need["name"]] = held.get(need["name"], [need["lo"], need["hi"]])
    # The same spans through the eighth version's fill, called directly.
    index = cardrules.WindowIndex({}, [(r["video"], r["start"], r["end"]) for r in rows], vectors, np.zeros(len(rows), bool))
    footage = fillrules.Footage(cards=request["cards"], catalog=catalog, index=index, source=lambda v: v, turned=set())
    direct, short = fillrules.fill(music, planned, picked, footage,
                                   lambda beat, _f, _k, lo, hi, _t=False: tuple(request["known"][f"{beat}@{lo:.2f}-{hi:.2f}"]),
                                   fps=FPS, labels=request["labels"])
    fields = ("card", "beats", "start", "end", "slot", "slow", "part", "picture")
    if json.loads(json.dumps(direct)) != answer["clips"] or [list(x) for x in short] != answer["short"]:
        return False, f"{key}: the Worker's fill differs from the eighth version's called directly"
    if unmeasured:
        return (False, f"{key}: {len(answer['clips'])} clips identical to the eighth version's fill called directly; "
                f"the prototype's cut came from an older run ({len(unmeasured)} spans it never measured)")
    got, want = answer["clips"], cut["clips"]
    differ = [n for n, (a, b) in enumerate(zip(got, want, strict=False), 1)
              if not all(same(a.get(f), b.get(f)) for f in fields)]
    if len(got) != len(want) or differ:
        return False, f"{key}: {len(got)} clips against {len(want)}; first differing clip {differ[:1]}"
    return True, f"{key}: {len(got)} clips identical, {calls} fill calls, short {answer['short']}"


def cards_case(root, key, index):
    _, rows, vectors, flat = window_index(root, index)
    want = load(root / CARDS / f"cards-{key}.json")
    # pj's cards were cut again by hand around the dolphins afterwards.
    if (root / CARDS / f"cards-{key}.before-dolphins.json").exists():
        want = load(root / CARDS / f"cards-{key}.before-dolphins.json")
    by_video = {}
    for n, row in enumerate(rows):
        by_video.setdefault(row["video"], []).append(n)
    got = []
    for video, members in sorted(by_video.items()):
        answer = worker.op_cards({"windows": [rows[n] for n in members], "key": key},
                                 {"vectors": vectors[members], "flat": flat[members]})
        got += [(video, c["start"], c["end"], c["times"]) for c in answer["cards"]]
    expected = [(c["video"], c["start"], c["end"], c["times"]) for c in want.values()]
    # Cards are matched by video and bounds: the prototype's files had cards
    # removed by hand afterwards (pj-097), which shifts the numbering.
    missing = [c for c in expected if not any(c[0] == g[0] and same(list(c[1:]), list(g[1:])) for g in got)]
    extra = [g for g in got if not any(g[0] == c[0] and same(list(c[1:]), list(g[1:])) for c in expected)]
    if missing:
        return False, f"{key}: {len(missing)} of the prototype's cards are not cut again, first {missing[0][:3]}"
    if extra:
        return False, f"{key}: {len(expected)} cards identical; {len(extra)} more that the prototype's file no longer has: {[g[:3] for g in extra]}"
    return True, f"{key}: {len(got)} cards identical"


def record_hash(path, root, hashed):
    if path not in hashed:
        with path.open("rb") as source:
            digest = hashlib.file_digest(source, "sha256").hexdigest()
        print(f"{digest}  {path.relative_to(root)}", flush=True)
        hashed.add(path)


def gray_frames(video, start, width, height, end=None):
    args = ["ffmpeg", "-v", "error", "-ss", f"{start:.3f}"]
    if end is not None:
        args += ["-t", f"{end - start:.3f}"]
    args += ["-i", str(video)]
    if end is None:
        args += ["-frames:v", "1"]
    filters = f"scale={width}:{height},format=gray"
    if end is not None:
        filters = "fps=10," + filters
    return np.frombuffer(subprocess.run([*args, "-vf", filters, "-f", "rawvideo", "-"],
                                        capture_output=True, check=True).stdout, np.uint8)


def quality_case(root, key, index, hashed):
    card_dir = root / CARDS
    windows = load(window_folder(root, index) / "index.json")["videos"]
    cards = load(card_dir / f"cards-{key}.json")
    measures = load(card_dir / f"quality-{key}.json")
    if not cards or set(cards) - set(measures):
        return False, f"{key}: incomplete prototype quality fixtures"
    for card in cards.values():
        measured = measures[card["id"]]
        video = root / "videos" / windows[card["video"]]["file"]
        record_hash(video, root, hashed)
        width, height = picture.SHAKE_SIZE
        frames = gray_frames(video, card["start"], width, height, card["end"]).reshape(-1, height, width)
        pairs = []
        for start in range(0, len(frames) - 1, 299):
            pairs.extend(worker.moves(frames[start:start + 300]))
        blocked, points = [], []
        for instant in card["times"]:
            frame = card_dir / "frames" / key / card["video"] / f"{round(instant * 1000):07d}.jpg"
            record_hash(frame, root, hashed)
            jpeg = np.frombuffer(frame.read_bytes(), np.uint8)
            part = worker.op_quality_part({"kind": "keyframe"}, {"jpeg": jpeg})
            blocked.append(part["blocked"])
            sharp = None
            if part["light"] <= 60:
                gray = gray_frames(video, instant, 1920, -2).reshape(-1, 1920)
                sharp = worker.op_quality_part({"kind": "sharp"}, {"frame": gray})["points"]
            points.append(sharp)
        # Blur values were measured on the same encoded keyframes by the
        # prototype's FFmpeg adapter and are retained in the result file.
        blurs = [float(v) if np.isfinite(v) else None for v in measured["blurs"]]
        actual = worker.op_quality({"blurs": blurs, "blocked": blocked, "points": points, "moves": pairs}, {})
        if actual["shake"] != measured["shake"] or actual["reasons"] != qualityrules.reasons(measured):
            return False, f"{key}: quality differs for {card['id']}"
    return True, f"{key}: shake and quality reasons identical for {len(cards)} cards"


def main():
    root = os.environ.get("ANAS_VIDEOCUT_SAMPLES")
    if not root:
        sys.exit("set ANAS_VIDEOCUT_SAMPLES to the folder with cache/ and 剪辑对比/")
    root = Path(root).expanduser()
    failed = False
    hashed = set()
    for key, index in CASES.items():
        card_dir, cut_dir = root / CARDS, root / CUTS
        folder = window_folder(root, index)
        cut = cut_dir / f"cut-{key}-recut-1.json"
        inputs = [folder / "index.json", folder / "frames.npz", cut, cut_dir / load(cut)["music"],
                  card_dir / "reviewed.json", card_dir / "excluded.json",
                  card_dir / f"cards-{key}.json", card_dir / f"quality-{key}.json",
                  cut_dir / f"shots-{key}-recut-1.json", cut_dir / f"pick-{key}-recut-1.json",
                  cut_dir / f"held-{key}.json"]
        before = card_dir / f"cards-{key}.before-dolphins.json"
        if before.exists():
            inputs.append(before)
        for path in inputs:
            record_hash(path, root, hashed)
        for passed, line in (cards_case(root, key, index), fill_case(root, key, index), quality_case(root, key, index, hashed)):
            failed |= not passed
            print(line, flush=True)
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
