"""The music analysis Worker against the eighth version's allin1 structure files (development machine and Experimental NAS).

The test songs and the structure files stay out of the repository
(docs/architecture/montage-music.md). For each structure-<song>.json under
--structures it decodes <song>.* under --songs with FFmpeg as the media
worker does for the montage center (stereo at 44.1 kHz and mono at 22050 Hz,
float32), starts the Worker as its unit runs it, hands it the two streams
through pipes over its socket, and waits for it to exit, so that each song is
timed and measured on its own: the Worker's peak resident memory, models
included, and how long the song took. Then it compares the answer with the
structure file:

- beats and downbeats: F-measure within ±70 ms (mir_eval's default window);
- sections: boundaries within ±0.5 s (F-measure), and the share of the song,
  sampled every 0.1 s, whose label is the same;
- the tempo.

Run it with the music runtime's Python (scripts/build-montage-music-runtime.sh), from ai/:

    python -m eval.videocut_music_allin1 --songs ~/Music/QQ音乐 \\
      --structures "~/A-NAS-datasets/剪辑对比/规划与成片" \\
      --allin1 MODELS/allin1 --allin1-manifest ../deploy/models/allin1-harmonix-all.json \\
      --demucs MODELS/demucs --demucs-manifest ../deploy/models/htdemucs.json --out results.json
"""

import argparse
import json
import os
import platform
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path

import numpy as np

WINDOW = 0.07
BOUNDARY = 0.5


def decode(ffmpeg, song, channels, rate):
    """The media worker's PCM output (internal/ffmpegworker/command.go, OpAudio with PCM)."""
    return subprocess.run([ffmpeg, "-nostdin", "-v", "error", "-i", str(song), "-map", "0:a:0", "-vn", "-sn", "-dn",
                           "-map_metadata", "-1", "-ac", str(channels), "-ar", str(rate), "-c:a", "pcm_f32le", "-f", "f32le",
                           "pipe:1"], capture_output=True, check=True).stdout


def f_measure(reference, estimated, window):
    """mir_eval's F-measure: each estimate matches at most one reference within the window."""
    reference, estimated = sorted(reference), sorted(estimated)
    if not reference or not estimated:
        return 0.0
    used, hits = set(), 0
    for t in estimated:
        best = min((k for k in range(len(reference)) if k not in used and abs(reference[k] - t) <= window),
                   key=lambda k: abs(reference[k] - t), default=None)
        if best is not None:
            used.add(best)
            hits += 1
    precision, recall = hits / len(estimated), hits / len(reference)
    return 0.0 if hits == 0 else 2 * precision * recall / (precision + recall)


def boundaries(segments):
    return sorted({s["start"] for s in segments if s["label"] not in ("start", "end")} |
                  {s["end"] for s in segments if s["label"] not in ("start", "end")})


def label_at(segments, t):
    return next((s["label"] for s in segments if s["start"] <= t < s["end"]), None)


def same_labels(reference, estimated, step=0.1):
    end = max(s["end"] for s in reference)
    times = np.arange(0, end, step)
    return float(np.mean([label_at(reference, t) == label_at(estimated, t) for t in times]))


def analyse(args, song):
    """One song through a Worker of its own: its answer, seconds and peak resident bytes."""
    stereo = decode(args.ffmpeg, song, 2, 44100)
    mono = decode(args.ffmpeg, song, 1, 22050)
    folder = tempfile.mkdtemp()
    path = os.path.join(folder, "music.sock")
    command = [args.python, "-m", "anas_montage_music.worker", "--socket", path, "--idle-seconds", "1",
               "--allin1", args.allin1, "--allin1-manifest", args.allin1_manifest,
               "--demucs", args.demucs, "--demucs-manifest", args.demucs_manifest]
    if args.threads:
        command += ["--threads", str(args.threads)]
    environment = {**os.environ, "PYTHONPATH": os.pathsep.join([str(Path(__file__).resolve().parents[1]), os.environ.get("PYTHONPATH", "")]),
                   "HF_HUB_OFFLINE": "1"}
    started = time.monotonic()
    # The Worker's log goes to a file: a pipe nobody reads would stop it.
    log = open(os.path.join(folder, "worker.log"), "w+b")  # noqa: SIM115
    worker = subprocess.Popen(command, env=environment, stderr=log)
    while not os.path.exists(path):
        if worker.poll() is not None:
            log.seek(0)
            raise RuntimeError(log.read().decode())
        time.sleep(0.05)
    pipes = [os.pipe(), os.pipe()]
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.connect(path)
        body = json.dumps({"op": "analyze", "streams": [{"name": "stereo", "channels": 2, "rate": 44100},
                                                         {"name": "mono", "channels": 1, "rate": 22050}]}).encode()
        socket.send_fds(conn, [struct.pack(">I", len(body)) + body], [r for r, _ in pipes])
        for r, _ in pipes:
            os.close(r)

        def feed():
            for (_, w), data in zip(pipes, (stereo, mono), strict=True):
                with os.fdopen(w, "wb") as out:
                    out.write(data)

        threading.Thread(target=feed, daemon=True).start()
        (length,) = struct.unpack(">I", conn.recv(4))
        data = b""
        while len(data) < length:
            data += conn.recv(length - len(data))
    answer = json.loads(data)
    _, _, usage = os.wait4(worker.pid, 0)
    seconds = time.monotonic() - started
    # ru_maxrss is in kilobytes on Linux and bytes on macOS.
    peak = usage.ru_maxrss * (1 if platform.system() == "Darwin" else 1024)
    log.seek(0)
    logged = log.read().decode()
    log.close()
    if not answer.get("ok"):
        raise RuntimeError(f"{song.name}: {answer} {logged[-2000:]}")
    answer["stages"] = next((line.split("INFO ", 1)[-1] for line in logged.splitlines() if "separated in" in line), "")
    return answer, seconds, peak, len(mono) / 4 / 22050


def check_separation(args, song):
    """separate against demucs' own path (worker.separation_matches_demucs), in this process."""
    from anas_montage_music import worker

    models = worker.Models(args.allin1_manifest, args.allin1, args.demucs_manifest, args.demucs, args.threads)
    _, separator = models.load()
    stereo = np.frombuffer(bytearray(decode(args.ffmpeg, song, 2, 44100)), "<f4").reshape(-1, 2)
    clip = stereo[: int(args.check_separation * 44100)]
    same = worker.separation_matches_demucs(separator, np.ascontiguousarray(clip))
    print(json.dumps({"song": song.stem, "separationMatchesDemucs": same}, ensure_ascii=False), flush=True)
    if not same:
        raise SystemExit(f"{song.name}: the Worker's separation differs from demucs'")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--songs", required=True)
    parser.add_argument("--structures", required=True)
    for name in ("allin1", "allin1-manifest", "demucs", "demucs-manifest"):
        parser.add_argument(f"--{name}", required=True)
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--ffmpeg", default="ffmpeg")
    parser.add_argument("--threads", type=int, default=0)
    parser.add_argument("--only", help="only the songs whose name contains this")
    parser.add_argument("--out")
    parser.add_argument("--check-separation", type=float, metavar="SECONDS",
                        help="first check that the Worker separates the first SECONDS of each song number for number as demucs does")
    args = parser.parse_args()
    results = []
    for path in sorted(Path(args.structures).expanduser().glob("structure-*.json")):
        name = path.stem.removeprefix("structure-")
        if args.only and args.only not in name:
            continue
        reference = json.loads(path.read_text(encoding="utf-8"))
        song = next(Path(args.songs).expanduser().glob(f"{name}.*"))
        if args.check_separation:
            check_separation(args, song)
        answer, seconds, peak, duration = analyse(args, song)
        row = {"song": name, "duration": round(duration, 1), "seconds": round(seconds, 1), "analysis": answer["seconds"],
               "peakMiB": round(peak / 2**20), "bpm": [reference["bpm"], answer["bpm"]],
               "beatF": round(f_measure(reference["beats"], answer["beats"], WINDOW), 3),
               "downbeatF": round(f_measure(reference["downbeats"], answer["downbeats"], WINDOW), 3),
               "boundaryF": round(f_measure(boundaries(reference["segments"]), boundaries(answer["segments"]), BOUNDARY), 3),
               "sameLabels": round(same_labels(reference["segments"], answer["segments"]), 3), "stages": answer["stages"],
               "structure": {k: answer[k] for k in ("bpm", "beats", "downbeats", "segments")}}
        results.append(row)
        print(json.dumps({k: v for k, v in row.items() if k != "structure"}, ensure_ascii=False), flush=True)
    if args.out:
        Path(args.out).write_text(json.dumps({"machine": platform.platform(), "processor": platform.processor() or platform.machine(),
                                              "cpus": os.cpu_count(), "results": results}, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
