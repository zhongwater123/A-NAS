"""The compute Worker's music operation (docs/architecture/videocut-center.md, 配乐).

VideoCut asks for a song's excerpt once allin1 has found its
structure, and again when the member drags its start. The answer is the
eighth version's (domain.music.from_structure): the excerpt around the first
chorus, its sections with their levels, and the slots the shots fill, cut on
the bar lines. On top of that, as the specification asks:

- no chorus found is not an error: the loudest section stands in for it, and
  the answer says so ("chorus": false) for the page to ask the member to
  listen;
- a start the member chose keeps the eighth version's sections and slots,
  and ends on the downbeat nearest the target length after it;
- a song that cannot be used says why ("unusable"): fewer than MIN_BARS bars,
  no excerpt of EXCERPT seconds, or a slot longer than LONGEST_SLOT, the most
  a segment may last.

Request: "structure" (allin1's bpm, beats, downbeats and segments, as the
eighth version's structure-<song>.json), "target" seconds, "start" (null for
the eighth version's choice), optional "song"; array "loudness" (float32, n),
the eighth version's RMS curve: frames of 2048 samples every 512 at 22050 Hz
(adapters/music.py, rms_curve).

worker.py serves it as OPS["music"].
"""

import itertools
import math

import numpy as np

from ..algo import music

# The eighth version's RMS curve: a value every HOP samples at RATE.
HOP, RATE = 512, 22050
# docs/specs/videocut-center.md, 配乐: when a song cannot be used.
MIN_BARS = 16
EXCERPT = (30.0, 150.0)
LONGEST_SLOT = 12.0
# The longest song VideoCut takes, with room for the analysis.
LONGEST_SONG = 8 * 60 + 5
LABELS = set(music.LEVELS)


class Refused(ValueError):
    """The request cannot be answered as it stands; the same request would fail again."""


def times_list(values, name):
    if not isinstance(values, list) or not all(isinstance(t, (int, float)) and math.isfinite(t) for t in values):
        raise Refused(f"{name} are not times")
    out = [float(t) for t in values]
    if any(t < 0 or t > LONGEST_SONG for t in out) or any(b <= a for a, b in itertools.pairwise(out)):
        raise Refused(f"{name} are not increasing times within the song")
    return out


def structure(raw, song=""):
    """allin1's structure, checked, as from_structure reads it."""
    if not isinstance(raw, dict):
        raise Refused("structure is an object")
    segments = raw.get("segments")
    if not isinstance(segments, list) or not segments:
        raise Refused("structure has no segments")
    sections = []
    for seg in segments:
        if not isinstance(seg, dict) or seg.get("label") not in LABELS:
            raise Refused(f"segment {seg!r} is not one allin1 labels")
        start, end = seg.get("start"), seg.get("end")
        if not all(isinstance(t, (int, float)) and math.isfinite(t) for t in (start, end)) or not 0 <= start <= end <= LONGEST_SONG:
            raise Refused(f"segment {seg!r} has no times")
        sections.append({"start": float(start), "end": float(end), "label": seg["label"]})
    bpm = raw.get("bpm", 0)
    if not isinstance(bpm, (int, float)) or not math.isfinite(bpm):
        raise Refused("bpm is a number")
    return {"song": song, "bpm": bpm, "beats": times_list(raw.get("beats", []), "beats"),
            "downbeats": times_list(raw.get("downbeats", []), "downbeats"), "segments": sections}


def loudest_as_chorus(st, rms, times):
    """The structure with its loudest section, of two bars or more, labelled the chorus."""
    bar = float(np.median(np.diff(st["downbeats"])))
    peak = float(rms.max())
    best, loudest = None, -math.inf
    for k, g in enumerate(st["segments"]):
        if g["label"] in ("start", "end") or g["end"] - g["start"] < 2 * bar:
            continue
        inside = rms[(times >= g["start"]) & (times < g["end"])]
        if not len(inside) or peak <= 0:
            continue
        level = 20 * math.log10(max(float(inside.mean()), 1e-12) / peak)
        if level > loudest:
            best, loudest = k, level
    if best is None:
        return None
    segments = [dict(g) for g in st["segments"]]
    segments[best]["label"] = "chorus"
    return {**st, "segments": segments}


def from_start(st, rms, times, target, start):
    """The excerpt from a downbeat the member chose, to the downbeat nearest target seconds later or the music's end.

    Its sections and slots are the eighth version's own (domain.music.sections and parts), as the default excerpt's."""
    segs, bar = music.sections(st, rms, times)
    bars = st["downbeats"]
    if not segs:
        return None
    start = min(bars, key=lambda t: abs(t - start))
    if start >= segs[-1]["end"] - 1.0:
        return None
    later = [t for t in bars if t > start + target - bar]
    end = min(later, key=lambda t: abs(t - start - target)) if later else segs[-1]["end"]
    end = min(end, segs[-1]["end"])
    i = next((k for k, g in enumerate(segs) if g["end"] > start), len(segs) - 1)
    j = next((k for k in range(len(segs) - 1, -1, -1) if segs[k]["start"] < end), i)
    return {"song": st["song"], "tempo": st["bpm"], "bar": round(bar, 2), "structure": st["segments"],
            "excerpt": [round(start, 2), round(end, 2)], "seconds": round(end - start, 2),
            "parts": music.parts(segs, i, j, start, end, bars)}


def unusable(m):
    """Why an excerpt cannot be used, or None."""
    if m is None or not m["parts"] or not EXCERPT[0] <= m["seconds"] <= EXCERPT[1]:
        return "no_excerpt"
    if max((b - a for part in m["parts"] for a, b in part["slots"]), default=math.inf) > LONGEST_SLOT:
        return "long_slot"
    return None


def op_music(request, arrays):
    st = structure(request.get("structure"), str(request.get("song", "")))
    rms = arrays["loudness"].astype(np.float32)
    if rms.ndim != 1 or not len(rms) or not np.isfinite(rms).all() or (rms < 0).any():
        raise Refused("loudness is one non-negative curve")
    times = np.arange(len(rms)) * HOP / RATE
    target = float(request.get("target", 90.0))
    if not EXCERPT[0] <= target <= EXCERPT[1]:
        raise Refused("target is between 30 and 150 seconds")
    start = request.get("start")
    if start is not None and (not isinstance(start, (int, float)) or not math.isfinite(start) or start < 0):
        raise Refused("start is a time in the song")
    base = {"chorus": True, "excerpt": [0.0, 0.0], "seconds": 0.0, "parts": [], "tempo": st["bpm"], "bar": 0.0}
    if len(st["downbeats"]) - 1 < MIN_BARS:
        return {**base, "unusable": "few_bars"}
    if float(rms.max()) <= 0:
        return {**base, "unusable": "no_excerpt"}

    def pick(st):
        if start is not None:
            return from_start(st, rms, times, target, float(start))
        return music.from_structure(st, rms, times, target)

    chorus = True
    try:
        m = pick(st)
    except StopIteration:
        # from_structure finds no chorus among the sections it keeps.
        chorus, stand_in = False, loudest_as_chorus(st, rms, times)
        try:
            m = pick(stand_in) if stand_in is not None else None
        except (StopIteration, ValueError):
            m = None
    except ValueError:
        # No downbeat to end on near the target.
        m = None
    if chorus and start is not None and not any(g["label"] == "chorus" for g in music.sections(st, rms, times)[0]):
        chorus = False
    reason = unusable(m)
    if m is None:
        return {**base, "chorus": chorus, "unusable": reason}
    return {**m, "chorus": chorus, **({"unusable": reason} if reason else {})}
