"""Golden comparison of the compute Worker's music operation with the eighth version (development machine only).

The ten test songs and the eighth version's allin1 structure files stay out
of the repository (docs/architecture/montage-center.md, 配乐). Point
ANAS_MONTAGE_SAMPLES at the folder that holds 剪辑对比/ (on development
machine C, ~/A-NAS-datasets) and ANAS_MONTAGE_SONGS at the songs (~/Music/QQ音乐),
and run, with NumPy and FFmpeg:

    python -m eval.videocut_music_golden

For each structure-<song>.json it decodes the song as the eighth version did
(adapters/music.py: FFmpeg, mono, 22050 Hz, float32) and as the montage
center does (the FFmpeg worker's PCM output, the same arguments), then
compares, excerpt and slot for slot:
- the eighth version's Allin1.music (domain.music.from_structure over its
  RMS curve), and
- the Worker's op_music, with the structure and loudness as the montage
  center sends them: the structure through JSON, the loudness as float32.
Where the eighth version kept a music-<song>.json from that run, it is
compared too; an older file (旧切点, 副歌未修正) is not.
"""

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

import numpy as np

from anas_videocut.algo import music
from anas_videocut.ops import music as music_ops

CUTS = "剪辑对比/规划与成片"
TARGET = 90.0


def samples(song):
    """The eighth version's decoding (adapters/music.py, samples)."""
    raw = subprocess.run([shutil.which("ffmpeg") or "/usr/bin/ffmpeg", "-v", "error", "-i", str(song), "-ac", "1", "-ar", "22050", "-f", "f32le", "-"],
                         capture_output=True, check=True).stdout
    return np.frombuffer(raw, np.float32)


def rms_curve(y):
    """The eighth version's RMS curve (adapters/music.py, rms_curve)."""
    padded = np.pad(y, 1024)
    n = 1 + (len(padded) - 2048) // 512
    frames = np.lib.stride_tricks.as_strided(padded, (n, 2048), (padded.strides[0] * 512, padded.strides[0]))
    return np.sqrt(np.mean(np.abs(frames) ** 2, axis=1)), np.arange(n) * 512 / 22050


def main():
    root = Path(os.environ.get("ANAS_MONTAGE_SAMPLES", "~/A-NAS-datasets")).expanduser() / CUTS
    songs = Path(os.environ.get("ANAS_MONTAGE_SONGS", "~/Music/QQ音乐")).expanduser()
    failures = 0
    for path in sorted(root.glob("structure-*.json")):
        st = json.loads(path.read_text(encoding="utf-8"))
        name = path.stem.removeprefix("structure-")
        song = next(songs.glob(f"{name}.*"))
        rms, times = rms_curve(samples(song))
        v8 = music.from_structure(st, rms, times, TARGET)
        sent = json.loads(json.dumps({k: st[k] for k in ("bpm", "beats", "downbeats", "segments")}))
        ours = music_ops.op_music({"structure": sent, "target": TARGET, "start": None, "song": st["song"]},
                                  {"loudness": np.frombuffer(rms.astype("<f4").tobytes(), "<f4")})
        same = {k: v for k, v in ours.items() if k not in ("chorus", "unusable")} == v8
        kept = path.with_name(f"music-{name}.json")
        stored = json.loads(kept.read_text(encoding="utf-8")) if kept.exists() else None
        matches_file = stored is None or all(stored[k] == v8[k] for k in ("excerpt", "seconds", "parts"))
        slots = sum(len(p["slots"]) for p in v8["parts"])
        print(f"{name}: excerpt {v8['excerpt']} {v8['seconds']} s, {len(v8['parts'])} parts, {slots} slots; "
              f"Worker {'same' if same else 'DIFFERENT'}; chorus {ours['chorus']}; unusable {ours.get('unusable')}; "
              f"music file {'none' if stored is None else 'same' if matches_file else 'DIFFERENT'}")
        failures += (not same) + (not matches_file)
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
