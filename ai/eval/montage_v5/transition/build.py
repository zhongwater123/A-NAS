"""The blind labelling site for detect.py's moments: each a 3-second clip around it, mixed with clips at random
times nothing was found near (to see what the rules miss), shuffled, the rule kept out of the page (key.json).

  python3 transition/build.py SIGNALS.json SITE/ [--per-rule 30] [--random 40]

SITE gets clips/<id>.mp4 (480 wide, no sound), data.js (the clips in order), key.json (each clip's rule) and
index.html (linked); transition/server.py serves it and keeps the marks.
"""
import argparse
import json
import random
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))  # montage_v5
from paths import VIDEOS, read_json  # noqa: E402

BEFORE, AFTER = 1.5, 1.5    # seconds of clip either side of the moment
APART = 1.0                 # two moments of one rule closer than this in one video are one
CLEAR = 3.0                 # a random clip this far from every moment


def moments(sig):
    out = []
    for video, v in sig["videos"].items():
        kept = []
        for e in sorted(v["events"], key=lambda e: -e["strength"]):
            if all(k["rule"] != e["rule"] or abs(k["t"] - e["t"]) >= APART for k in kept):
                kept.append(e)
        out += [{"video": video, **e} for e in kept if BEFORE <= e["t"] <= v["duration"] - 0.5]
    return out


def clip(file, t, turned, out):
    turn = "hflip,vflip," if turned else ""
    subprocess.run(["ffmpeg", "-hide_banner", "-v", "error", "-y", "-ss", f"{max(0.0, t - BEFORE):.3f}", "-t", f"{BEFORE + AFTER:.3f}",  # noqa: S607
                    "-i", str(file), "-an", "-vf", f"{turn}scale=480:-2", "-c:v", "libx264", "-preset", "veryfast", "-crf", "26",
                    "-pix_fmt", "yuv420p", "-movflags", "+faststart", str(out)], check=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("signals", type=Path)
    ap.add_argument("site", type=Path)
    ap.add_argument("--per-rule", type=int, default=30)
    ap.add_argument("--random", type=int, default=40)
    ap.add_argument("--seed", type=int, default=20261116)
    args = ap.parse_args()
    sig = read_json(args.signals)
    rng = random.Random(args.seed)  # noqa: S311 (a sample to label, not a secret)
    found = moments(sig)
    picked = []
    for rule in ("whip", "cover", "zoom", "tilt"):
        mine = [m for m in found if m["rule"] == rule]
        picked += rng.sample(mine, min(len(mine), args.per_rule))
    videos = list(sig["videos"].items())
    controls = []
    while len(controls) < args.random:
        video, v = rng.choice(videos)
        if v["duration"] < BEFORE + AFTER + 1:
            continue
        t = round(rng.uniform(BEFORE, v["duration"] - AFTER), 1)
        if all(abs(e["t"] - t) >= CLEAR for e in v["events"]) and all(c["video"] != video or abs(c["t"] - t) >= CLEAR for c in controls):
            controls.append({"video": video, "rule": "random", "t": t, "direction": "", "strength": 0})
    items = picked + controls
    rng.shuffle(items)
    (args.site / "clips").mkdir(parents=True, exist_ok=True)
    data, key = [], {}
    for n, m in enumerate(items, 1):
        cid = f"t{n:03d}"
        v = sig["videos"][m["video"]]
        out = args.site / "clips" / f"{cid}.mp4"
        if not out.exists():
            clip(VIDEOS / v["file"], m["t"], v["turned"], out)
        data.append({"id": cid, "clip": f"clips/{cid}.mp4", "video": m["video"], "t": m["t"]})
        key[cid] = {k: m[k] for k in ("video", "rule", "t", "direction", "strength")}
        print(cid, m["rule"], m["video"], m["t"], flush=True)
    (args.site / "data.js").write_text("window.ITEMS = " + json.dumps(data, ensure_ascii=False) + ";\n", encoding="utf-8")
    (args.site / "key.json").write_text(json.dumps(key, ensure_ascii=False, indent=1), encoding="utf-8")
    page = args.site / "index.html"
    if not page.exists():
        page.symlink_to(HERE / "index.html")
    counts = {r: sum(m["rule"] == r for m in found) for r in ("whip", "cover", "zoom", "tilt")}
    print("found", counts, "picked", len(picked), "random", len(controls))


if __name__ == "__main__":
    main()
