"""A song's slots for a montage, CutClaw's way: the music is the cut's time line. allin1 (structure.py)
names the song's sections and gives its downbeats; a chorus is the peak, after a build when the section
after it is louder (BUILD_RISE). The excerpt is whole sections holding the first chorus, about the target
long; its slots are cut points on bar lines: every bar in a chorus, every two bars elsewhere.

(Loudness alone does not find the chorus: keshi's Forever peaked at its last loud stretch, and its intro is
louder than its first chorus. Clustering beat-synchronous chroma and MFCC split Fortnight into 4 s pieces
or one 90 s section: its texture hardly changes.)

  python music.py SONG [TARGET_SECONDS]     (needs structure-<stem>.json from structure.py; writes
                                             music-<stem>.json under paths.CUTS)
"""

import itertools
import json
import subprocess
import sys
from pathlib import Path

import librosa
import numpy as np

sys.path.insert(0, str(Path(__file__).resolve().parent))
from paths import CUTS

SR = 22050
PHRASE = 4                  # bars a phrase


def samples(path):
    """The song as mono floats at SR, decoded by FFmpeg (libsndfile could not read some MP3s)."""
    raw = subprocess.run(["ffmpeg", "-v", "error", "-i", str(path), "-ac", "1", "-ar", str(SR), "-f", "f32le", "-"],  # noqa: S607
                         capture_output=True, check=True).stdout
    return np.frombuffer(raw, np.float32)


# allin1's labels (structure.py) as the montage's levels: a chorus is the peak; a verse, bridge, instrumental
# or solo is 中等; an intro, outro, break, start or end is 安静.
LABELS = {"chorus": "高潮", "verse": "中等", "bridge": "中等", "inst": "中等", "solo": "中等", "build": "中等",
          "intro": "安静", "outro": "安静", "break": "安静", "start": "安静", "end": "安静"}
# A section allin1 calls a chorus, followed by one at least BUILD_RISE dB louder, is the build before a drop:
# the louder one is the chorus, whatever allin1 calls it. Forester's Symphony: chorus at 47.8 s (-9.2 dB),
# then 63.7 s (-4.4 dB), where the user hears the chorus (1:03); All My Days: chorus at 61.2 s (-6.5 dB),
# then "inst" at 77.6 s (-4.3 dB), the user's 1:15. Forever's and Fortnight's choruses are as loud as what
# follows them (+0.1 dB, 0 dB) and keep their labels (2026-11-11).
BUILD_RISE = 2.0


def from_structure(path, target):
    """The excerpt and slots from allin1's sections and downbeats (structure-<stem>.json): whole sections
    holding the first chorus (after BUILD_RISE), nearest the target and then earliest; when whole sections run over by more
    than a phrase, the last is cut at a downbeat. Slots on downbeats: every bar in a chorus, every two
    elsewhere. (By loudness, keshi's Forever peaked at its last loud stretch, not its first chorus at 49 s.)"""
    st = json.loads(path.read_text(encoding="utf-8"))
    bars = st["downbeats"]
    bar = float(np.median(np.diff(bars)))
    # A section under two bars is joined to the next (Fortnight's first 2.8 s came out a verse of its own,
    # one slot; a part of the cut cannot be made of one picture of that).
    segs = []
    for g in (g for g in st["segments"] if g["label"] not in ("start", "end") and g["end"] - g["start"] > 1.0):
        if segs and segs[-1]["end"] - segs[-1]["start"] < 2 * bar:
            segs[-1] = {**g, "start": segs[-1]["start"]}
        else:
            segs.append(dict(g))
    # Bounds on the nearest downbeat (Fortnight's chorus came at 34.1 s, half a bar off, leaving 1.2 s slots
    # either side of it).
    for g, nxt in itertools.pairwise(segs):
        t = min(bars, key=lambda x: abs(x - nxt["start"]))
        if abs(t - nxt["start"]) <= bar / 2:
            g["end"] = nxt["start"] = t
    rms = librosa.feature.rms(y=samples(st["song"]))[0]
    times = librosa.times_like(rms, sr=SR)

    def loudness(g):
        return 20 * float(np.log10(rms[(times >= g["start"]) & (times < g["end"])].mean() / rms.max()))

    for g, nxt in itertools.pairwise(segs):
        if g["label"] == "chorus" and loudness(nxt) - loudness(g) >= BUILD_RISE:
            g |= {"allin1": g["label"], "label": "build"}
            nxt |= {"allin1": nxt["label"], "label": "chorus"}
    first = next(k for k, g in enumerate(segs) if g["label"] == "chorus")
    options = [(i, j) for i in range(first + 1) for j in range(first, len(segs))]
    i, j = min(options, key=lambda o: (max(0.0, abs(segs[o[1]]["end"] - segs[o[0]]["start"] - target) - bar * PHRASE),
                                       segs[o[0]]["start"]))
    start, end = segs[i]["start"], segs[j]["end"]
    if end - start > target + bar * PHRASE:
        end = min((t for t in bars if t > start + target - bar), key=lambda t: abs(t - start - target))
    parts = []
    for n, g in enumerate(segs[i:j + 1], i):
        a, b = max(g["start"], start), min(g["end"], end)
        if b - a < 1.0:
            continue
        level = LABELS.get(g["label"], "中等")
        # Every second bar line from the section's start: [1::2], the 2nd, 4th, 6th (2026-11-10: [::2] cut on
        # the 1st, 3rd, 5th, leaving a one-bar shot at each end of the section, and the cut's last shot a
        # bar short of the rest). In the excerpt's last section they count back from its end, so that an odd
        # bar falls at its start and the cut's last shot is whole (Fortnight's last verse is seven bars).
        inside = [t for t in bars if a + 0.5 < t < b - 0.5]
        lines = inside if level == "高潮" else inside[::-1][1::2][::-1] if n == j else inside[1::2]
        cuts = [a, *lines, b]
        parts.append({"start": round(a - start, 2), "end": round(b - start, 2), "level": level, "label": g["label"],
                      **({"allin1": g["allin1"]} if "allin1" in g else {}),
                      "slots": [[round(x - start, 2), round(y - start, 2)] for x, y in itertools.pairwise(cuts) if y - x > 0.5]})
    return {"song": st["song"], "tempo": st["bpm"], "bar": round(bar, 2), "structure": st["segments"],
            "excerpt": [round(start, 2), round(end, 2)], "seconds": round(end - start, 2), "parts": parts}


def main():
    song = Path(sys.argv[1])
    target = float(sys.argv[2]) if len(sys.argv) > 2 else 90.0
    found = CUTS / f"structure-{song.stem}.json"
    if not found.exists():
        sys.exit(f"先用 structure.py 分析 {song.name}")
    out = from_structure(found, target)
    path = CUTS / f"music-{song.stem}.json"
    path.write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
    print(path)
    print("速度", out["tempo"], "BPM，一小节", out["bar"], "秒")
    print("allin1 段落", [f"{g['start']:.0f}-{g['end']:.0f} {g['label']}" for g in out["structure"]])
    print("选段", out["excerpt"], out["seconds"], "秒")
    for n, p in enumerate(out["parts"], 1):
        print(f"  段{n} {p['start']:.1f}–{p['end']:.1f} {p['level']} {p['label']} {len(p['slots'])} 个镜头槽", [round(b - a, 1) for a, b in p["slots"]])


if __name__ == "__main__":
    main()
