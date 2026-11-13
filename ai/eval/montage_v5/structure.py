"""第六版 (2026-11-09): a song's sections with their names (intro, verse, chorus, bridge, outro), beats and
downbeats by All-In-One (allin1, Kim & Nam 2023: source separation, then a transformer over the stems;
MIT). Loudness alone made the last loud stretch of keshi's Forever its peak, not its first chorus at 49 s.

  python structure.py SONG ...     (in a venv with allin1; see README)
      (writes structure-<stem>.json under paths.CUTS; the separated stems are not kept)

A script file with a main guard: allin1 spreads its spectrograms over processes, which on macOS start by
importing the main module, and a script read from stdin cannot be imported again (the children respawned).
"""

import json
import os
import sys
import tempfile
from pathlib import Path

import allin1
from paths import CUTS  # beside this script, which Python puts first on the path

# The 8-fold ensemble (harmonix-all) takes minutes a song on this CPU (NATTEN has no Apple GPU kernels);
# STRUCTURE_MODEL=harmonix-fold0 runs one fold, about an eighth of it.
MODEL = os.environ.get("STRUCTURE_MODEL", "harmonix-all")


def main():
    with tempfile.TemporaryDirectory() as work:
        for song in map(Path, sys.argv[1:]):
            r = allin1.analyze(str(song), out_dir=None, demix_dir=f"{work}/demix", spec_dir=f"{work}/spec",
                               device="cpu", keep_byproducts=False, model=MODEL)
            out = {"song": str(song), "model": MODEL, "bpm": r.bpm, "beats": [round(float(t), 3) for t in r.beats],
                   "downbeats": [round(float(t), 3) for t in r.downbeats],
                   "segments": [{"start": round(float(s.start), 2), "end": round(float(s.end), 2), "label": s.label}
                                for s in r.segments]}
            path = CUTS / f"structure-{song.stem}.json"
            path.write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
            print(song.stem, "BPM", r.bpm, flush=True)
            for s in out["segments"]:
                print(f"  {s['start']:6.1f}–{s['end']:6.1f} {s['label']}", flush=True)


if __name__ == "__main__":
    main()
