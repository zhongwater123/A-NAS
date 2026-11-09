"""第六版 (2026-11-09): a song's slots for a montage, CutClaw's way (docs/research/video-editing-cutclaw.md):
the music is the cut's time line. librosa (madmom, CutClaw's, is not kept up for this Python) gives the
beats; bars are four beats from the phase whose beats carry the most onset strength; phrases are four bars.
CutClaw has Qwen-Omni listen and name the sections; here a section is a run of phrases of one level, the
level each phrase's loudness against the excerpt's: 安静 (lowest third), 高潮 (top quarter) or 中等.
(Clustering beat-synchronous chroma and MFCC, librosa's agglomerative and its Laplacian segmentation, split
Fortnight into 4 s pieces or one 90 s section: its texture hardly changes.)

The excerpt is whole phrases about the target long, holding the loudest phrase, ending at the song's end
when it can (a real ending), else as early as it can. Its slots are cut points on bar lines: every bar in
a 高潮 section, every two bars elsewhere.

  ~/A-NAS-datasets/tools/music-venv/bin/python music.py SONG [TARGET_SECONDS]   (writes music-<stem>.json under paths.CUTS)
"""

import itertools
import json
import sys
from pathlib import Path

import librosa
import numpy as np

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from paths import CUTS  # noqa: E402

SR = 22050
PHRASE = 4                  # bars a phrase
LEVELS = ("安静", "中等", "高潮")


def analyse(path, target):
    y, sr = librosa.load(path, sr=SR, mono=True)
    duration = len(y) / sr
    onset = librosa.onset.onset_strength(y=y, sr=sr)
    tempo, beat_frames = librosa.beat.beat_track(onset_envelope=onset, sr=sr)
    beats = librosa.frames_to_time(beat_frames, sr=sr)
    phase = max(range(4), key=lambda p: float(onset[beat_frames[p::4]].sum()))
    bars = [float(t) for t in beats[phase::4]]
    bar = float(np.median(np.diff(bars)))
    # Bar lines on past the last tracked beat (Fortnight's outro, 185-229 s, gives none) and before the first.
    while bars[-1] + bar < duration - 0.5:
        bars.append(bars[-1] + bar)
    while bars[0] - bar > 0.5:
        bars.insert(0, bars[0] - bar)
    rms = librosa.feature.rms(y=y)[0]
    times = librosa.times_like(rms, sr=sr)

    def loudness(a, b):
        m = (times >= a) & (times < b)
        return float(librosa.amplitude_to_db(np.array([rms[m].mean()]), ref=float(rms.max()))[0])

    starts = bars[::PHRASE]
    phrases = [(a, b, loudness(a, b)) for a, b in zip(starts, [*starts[1:], duration], strict=False) if b - a > bar]
    # The excerpt: phrases i..j, about the target, holding the loudest phrase; the song's end first.
    top = max(range(len(phrases)), key=lambda k: phrases[k][2])
    options = [(i, j) for i in range(len(phrases)) for j in range(i, len(phrases))
               if i <= top <= j and abs(phrases[j][1] - phrases[i][0] - target) <= bar * PHRASE]
    i, j = min(options, key=lambda o: (o[1] != len(phrases) - 1, abs(phrases[o[1]][1] - phrases[o[0]][0] - target), o[0]))
    picked = phrases[i:j + 1]
    start, end = picked[0][0], picked[-1][1]
    values = [v for _a, _b, v in picked]
    lo, hi = np.percentile(values, 33), np.percentile(values, 75)
    level = [LEVELS[2] if v >= hi else LEVELS[0] if v < lo else LEVELS[1] for v in values]
    parts = []
    for (a, b, v), lv in zip(picked, level, strict=True):
        if parts and parts[-1]["level"] == lv:
            parts[-1]["end"], parts[-1]["_v"] = b, parts[-1]["_v"] + [v]
        else:
            parts.append({"start": a, "end": b, "level": lv, "_v": [v]})
    for part in parts:
        step = 1 if part["level"] == "高潮" else 2
        lines = [t for t in bars if part["start"] + 0.5 < t < part["end"] - 0.5][step - 1::step] if step > 1 else \
            [t for t in bars if part["start"] + 0.5 < t < part["end"] - 0.5]
        cuts = [part["start"], *lines, part["end"]]
        part["slots"] = [[round(a - start, 2), round(b - start, 2)] for a, b in itertools.pairwise(cuts) if b - a > 0.5]
        part["loudness"] = round(float(np.mean(part.pop("_v"))), 1)
        part["start"], part["end"] = round(part["start"] - start, 2), round(part["end"] - start, 2)
    return {"song": str(path), "duration": round(duration, 2), "tempo": round(float(np.atleast_1d(tempo)[0]), 1),
            "bar": round(bar, 2), "phrases": [[round(a, 2), round(b, 2), round(v, 1)] for a, b, v in phrases],
            "excerpt": [round(start, 2), round(end, 2)], "seconds": round(end - start, 2), "parts": parts}


# allin1's labels (structure.py) as the montage's levels: a chorus is the peak; a verse, bridge, instrumental
# or solo is 中等; an intro, outro, break, start or end is 安静.
LABELS = {"chorus": "高潮", "verse": "中等", "bridge": "中等", "inst": "中等", "solo": "中等",
          "intro": "安静", "outro": "安静", "break": "安静", "start": "安静", "end": "安静"}


def from_structure(path, target):
    """The excerpt and slots from allin1's sections and downbeats (structure-<stem>.json): whole sections
    holding the first chorus, nearest the target and then earliest; when whole sections run over by more
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
    first = next(k for k, g in enumerate(segs) if g["label"] == "chorus")
    options = [(i, j) for i in range(first + 1) for j in range(first, len(segs))]
    i, j = min(options, key=lambda o: (max(0.0, abs(segs[o[1]]["end"] - segs[o[0]]["start"] - target) - bar * PHRASE),
                                       segs[o[0]]["start"]))
    start, end = segs[i]["start"], segs[j]["end"]
    if end - start > target + bar * PHRASE:
        end = min((t for t in bars if t > start + target - bar), key=lambda t: abs(t - start - target))
    parts = []
    for g in segs[i:j + 1]:
        a, b = max(g["start"], start), min(g["end"], end)
        if b - a < 1.0:
            continue
        level = LABELS.get(g["label"], "中等")
        lines = [t for t in bars if a + 0.5 < t < b - 0.5][::1 if level == "高潮" else 2]
        cuts = [a, *lines, b]
        parts.append({"start": round(a - start, 2), "end": round(b - start, 2), "level": level, "label": g["label"],
                      "slots": [[round(x - start, 2), round(y - start, 2)] for x, y in itertools.pairwise(cuts) if y - x > 0.5]})
    return {"song": st["song"], "tempo": st["bpm"], "bar": round(bar, 2), "structure": st["segments"],
            "excerpt": [round(start, 2), round(end, 2)], "seconds": round(end - start, 2), "parts": parts}


def main():
    song = Path(sys.argv[1])
    target = float(sys.argv[2]) if len(sys.argv) > 2 else 90.0
    found = CUTS / f"structure-{song.stem}.json"
    out = from_structure(found, target) if found.exists() else analyse(song, target)
    path = CUTS / f"music-{song.stem}.json"
    path.write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
    print(path)
    print("速度", out["tempo"], "BPM，一小节", out["bar"], "秒")
    if "structure" in out:
        print("allin1 段落", [f"{g['start']:.0f}-{g['end']:.0f} {g['label']}" for g in out["structure"]])
    else:
        print("乐句响度", [f"{a:.0f}:{v}" for a, _b, v in out["phrases"]])
    print("选段", out["excerpt"], out["seconds"], "秒")
    for n, p in enumerate(out["parts"], 1):
        print(f"  段{n} {p['start']:.1f}–{p['end']:.1f} {p['level']} {p.get('label', str(p.get('loudness')) + 'dB')} {len(p['slots'])} 个镜头槽", [round(b - a, 1) for a, b in p["slots"]])


if __name__ == "__main__":
    main()
