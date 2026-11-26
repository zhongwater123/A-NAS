"""VideoCut's music analysis Worker (docs/architecture/montage-center.md, 配乐).

It runs allin1's harmonix-all on one song at a time and answers the song's
tempo, beats, downbeats and labelled sections, as the eighth version's
structure-<song>.json, with the song's loudness curve. allin1 needs PyTorch,
Demucs, NATTEN and madmom, which the AI Worker's runtime does not have, so it
runs from a runtime of its own (scripts/build-videocut-music-runtime.sh). It has no
network and no data volume: the song arrives as two streams of samples, and
the models from the content the device installed, checked against their
manifests' digests.

The steps are allin1.analyze's, kept in memory instead of files:
1. Demucs (htdemucs) separates the stereo samples into bass, drums, other and
   vocals, as `demucs.separate` does with its defaults: shifts 1, overlap
   0.25, split, clipping rescaled, written as 16-bit PCM;
2. each stem is mixed down to mono as madmom reads a 16-bit WAV, and turned
   into allin1's log-filtered spectrogram (allin1.spectrogram);
3. the eight folds' ensemble reads the four spectrograms; madmom's DBN finds
   the downbeats among the beats (allin1.helpers.run_inference).
Demucs shifts the song by a random amount; the Worker seeds that, so the same
song always gives the same answer. The loudness is the eighth version's RMS
curve over the mono samples (montage_v8/adapters/music.py, rms_curve).

Each connection carries one request and one answer, framed as the AI
Worker's are (anas_ai.protocol). Operations:
  info     the models it runs and their digests.
  analyze  "streams" [{"name": "stereo", "channels": 2, "rate": 44100},
           {"name": "mono", "channels": 1, "rate": 22050}] as two descriptors,
           read to their end in that order: little-endian float32,
           interleaved. Answers "model", "bpm", "beats", "downbeats",
           "segments" [{start, end, label}], "loudness" (base64 of
           little-endian float32) and "seconds" it took.
           With "confirm": true the caller sends a second frame once it has
           written both streams, {"streams": "complete"}, and the song is
           analysed only then: a stream that ends does not tell a whole song
           from one whose decoding broke off. A caller that hangs up instead
           gets no answer, and nothing is analysed.
"""

import argparse
import base64
import contextlib
import hashlib
import json
import logging
import os
import random
import socket
import sys
import time
import warnings
from pathlib import Path

import numpy as np

from anas_ai import protocol

log = logging.getLogger("anas_videocut_music")

SYSTEMD_FIRST_FD = 3
IDLE_SECONDS = 300
# The longest song VideoCut takes, with room for the decoder.
LONGEST_SECONDS = 8 * 60 + 5
STEREO_RATE, MONO_RATE = 44100, 22050
# The eighth version's RMS curve (librosa.feature.rms's defaults).
FRAME, HOP = 2048, 512
# Fewer samples than this are no song.
SHORTEST_SECONDS = 1.0
SEED = 0
# allin1's harmonix-all: its eight folds, as allin1.models.loaders names them.
FOLDS = [
    "harmonix-fold0-0vra4ys2.pth", "harmonix-fold1-3ozjhtsj.pth", "harmonix-fold2-gmgo0nsy.pth",
    "harmonix-fold3-i92b7m8p.pth", "harmonix-fold4-1bql5qo0.pth", "harmonix-fold5-x4z5zeef.pth",
    "harmonix-fold6-x7t226rq.pth", "harmonix-fold7-qwwskhg6.pth",
]
# Demucs' htdemucs bag: one model (demucs/remote/htdemucs.yaml).
HTDEMUCS = "955717e8.safetensors"
STEMS = ("bass", "drums", "other", "vocals")


class Refused(Exception):
    """The request cannot be answered as it stands; the same request would fail again."""


class Dropped(Exception):
    """The caller gave the request up; it waits for no answer."""


def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def checked_files(manifest_path, folder):
    """The model's files in folder, each checked against the manifest's digest (deploy/models)."""
    manifest = json.loads(Path(manifest_path).read_text())
    parts = manifest.get("parts") or [{"file": manifest["file"], "sha256": manifest["sha256"]}]
    files = {}
    for part in parts:
        path = Path(folder) / part["file"]
        if digest(path) != part["sha256"]:
            raise RuntimeError(f"{path} does not match {manifest_path}")
        files[part["file"]] = path
    return manifest, files


class Models:
    """allin1's ensemble and Demucs, loaded on first use."""

    def __init__(self, allin1_manifest, allin1_dir, demucs_manifest, demucs_dir, threads):
        self.paths = (allin1_manifest, allin1_dir, demucs_manifest, demucs_dir)
        self.threads = threads
        self.loaded = None
        self.identity = None

    def load(self):
        if self.loaded is not None:
            return self.loaded
        import torch
        from allin1.models.allinone import AllInOne
        from allin1.models.ensemble import Ensemble
        from demucs.apply import BagOfModels
        from demucs.hf import load_safetensors_model
        from omegaconf import OmegaConf

        # NATTEN warns on every call that allin1's ops are deprecated, and
        # PyTorch about decorators; neither tells the device anything.
        logging.getLogger("natten").setLevel(logging.ERROR)
        warnings.filterwarnings("ignore", category=FutureWarning)
        warnings.filterwarnings("ignore", category=DeprecationWarning)
        allin1_manifest, allin1_dir, demucs_manifest, demucs_dir = self.paths
        allin1_pin, folds = checked_files(allin1_manifest, allin1_dir)
        demucs_pin, demucs_files = checked_files(demucs_manifest, demucs_dir)
        if sorted(folds) != sorted(FOLDS) or HTDEMUCS not in demucs_files:
            raise RuntimeError("the manifests do not name allin1's eight folds and htdemucs")
        if self.threads:
            torch.set_num_threads(self.threads)
        models = []
        for name in FOLDS:
            # The files are the manifests' own, checked above.
            checkpoint = torch.load(folds[name], map_location="cpu", weights_only=False)
            model = AllInOne(OmegaConf.create(checkpoint["config"]))
            model.load_state_dict(checkpoint["state_dict"])
            model.eval()
            models.append(model)
        ensemble = Ensemble(models).eval()
        separator = BagOfModels([load_safetensors_model(demucs_files[HTDEMUCS])])
        separator.eval()
        self.identity = f"allin1 harmonix-all {allin1_pin['sha256'][:12]}, htdemucs {demucs_pin['sha256'][:12]}"
        self.loaded = (ensemble, separator)
        return self.loaded


def read_stream(fd, channels, seconds=LONGEST_SECONDS, rate=STEREO_RATE):
    """A stream's samples to its end: (n, channels) float32."""
    most = int(seconds * rate) * channels * 4
    chunks, total = [], 0
    with os.fdopen(os.dup(fd), "rb", buffering=0) as stream:
        while True:
            chunk = stream.read(1 << 20)
            if not chunk:
                break
            total += len(chunk)
            if total > most:
                raise Refused("the song is longer than VideoCut takes")
            chunks.append(chunk)
    # Writable, for PyTorch to take as it is.
    data = bytearray().join(chunks)
    del chunks
    if len(data) % (4 * channels):
        raise Refused("the stream is not whole float32 frames")
    samples = np.frombuffer(data, "<f4").reshape(-1, channels)
    if len(samples) < SHORTEST_SECONDS * rate:
        raise Refused("the stream holds no song")
    if not np.isfinite(samples).all():
        raise Refused("the stream has samples that are not numbers")
    return samples


def high_water():
    """The process's peak resident memory so far in MiB (Linux), or 0 elsewhere."""
    try:
        with open("/proc/self/status") as status:
            line = next(line for line in status if line.startswith("VmHWM:"))
        return int(line.split()[1]) // 1024
    except (OSError, StopIteration, ValueError):
        return 0


def rms_curve(y):
    """The eighth version's loudness (its adapters/music.py, rms_curve): frames centred on every HOP-th sample, zero-padded at both ends."""
    import librosa

    return librosa.feature.rms(y=y, frame_length=FRAME, hop_length=HOP, center=True, pad_mode="constant")[0]


def spectrogram_processor():
    """allin1.spectrogram's chain, copied from madmom."""
    from madmom.audio.signal import FramedSignalProcessor
    from madmom.audio.spectrogram import FilteredSpectrogramProcessor, LogarithmicSpectrogramProcessor
    from madmom.audio.stft import ShortTimeFourierTransformProcessor
    from madmom.processors import SequentialProcessor

    return SequentialProcessor([
        FramedSignalProcessor(frame_size=2048, fps=int(44100 / 441)),
        ShortTimeFourierTransformProcessor(),
        FilteredSpectrogramProcessor(num_bands=12, fmin=30, fmax=17000, norm_filters=True),
        LogarithmicSpectrogramProcessor(mul=1, add=1),
    ])


def as_wave_file(stem):
    """A stem as madmom reads demucs' 16-bit WAV of it with num_channels=1.

    demucs.audio.save_audio rescales a stem that would clip, then writes
    clamp(-1, 1) * (2**15 - 1) truncated to int16 (audio._pcm_bytes); madmom
    mixes the two channels down by their mean, truncated to int16
    (madmom.audio.signal.remix).
    """
    from demucs.audio import prevent_clip
    from madmom.audio.signal import Signal

    stem = prevent_clip(stem, mode="rescale")
    frames = stem.clamp(-1, 1).t().contiguous().cpu().numpy()
    pcm = (frames * (2**15 - 1)).astype("<i2")
    mono = np.mean(pcm, axis=-1).astype(pcm.dtype)
    return Signal(mono, sample_rate=STEREO_RATE, num_channels=1)


def separate(separator, stereo):
    """demucs.api.Separator.separate_tensor with demucs.separate's defaults, number for number, in less memory.

    htdemucs is a bag of one model with weight 1, so the bag's averaging
    (demucs.apply.apply_model) changes no value; neither does dividing the
    one shift's output by one. Calling the model's split pass directly with
    the same random shift, keeping its output as a view and undoing the
    normalisation in place leaves out three copies of the separated song,
    some 1.4 MB a second of it each. separation_matches_demucs checks this.
    """
    import torch
    from demucs.apply import TensorChunk, apply_model, tensor_chunk

    (model,) = separator.models
    random.seed(SEED)
    torch.manual_seed(SEED)
    # The samples as (channels, length): a view, not a copy.
    wav = torch.from_numpy(stereo).T
    ref = wav.mean(0)
    mean, std = ref.mean(), ref.std() + 1e-8
    del ref
    with torch.no_grad():
        mix = ((wav - mean) / std).contiguous()[None]
        del wav
        length = mix.shape[-1]
        max_shift = int(0.5 * model.samplerate)
        padded = tensor_chunk(mix).padded(length + 2 * max_shift)
        offset = random.randint(0, max_shift)  # noqa: S311 (demucs' shift, not a secret)
        shifted = TensorChunk(padded, offset, length + max_shift - offset)
        out = apply_model(model, shifted, shifts=0, split=True, overlap=0.25, device="cpu", num_workers=0, progress=False)
        del mix, padded, shifted
        out = out[..., max_shift - offset:]
        out.mul_(std).add_(mean)
    return out


def separation_matches_demucs(separator, stereo):
    """separate's output against demucs' own path, for a short clip (ai/eval)."""
    import torch
    from demucs.apply import apply_model

    random.seed(SEED)
    torch.manual_seed(SEED)
    wav = torch.from_numpy(np.ascontiguousarray(stereo.T))
    ref = wav.mean(0)
    mean, std = ref.mean(), ref.std() + 1e-8
    with torch.no_grad():
        theirs = apply_model(separator, ((wav - mean) / std)[None], segment=None, shifts=1, split=True, overlap=0.25,
                             device="cpu", num_workers=0, progress=False)
    theirs = theirs * std + mean
    return bool(torch.equal(theirs, separate(separator, stereo)))


def structure(models, stereo):
    """allin1.analyze for one song held in memory."""
    import torch
    from allin1.postprocessing import (
        estimate_tempo_from_beats,
        postprocess_functional_structure,
        postprocess_metrical_structure,
    )
    ensemble, separator = models.load()
    clock = time.monotonic()
    out = separate(separator, stereo)
    separated, clock = time.monotonic() - clock, time.monotonic()
    peaks = [high_water()]
    stems = dict(zip(separator.sources, out[0], strict=True))
    # Each stem as the 16-bit mono madmom reads, then the float stems go
    # before the spectrograms are made: the same numbers in less memory.
    waves = [as_wave_file(stems[name]) for name in STEMS]
    del out, stems
    processor = spectrogram_processor()
    spec = np.stack([processor(wave) for wave in waves])
    del waves
    spectra, clock = time.monotonic() - clock, time.monotonic()
    peaks.append(high_water())
    with torch.no_grad():
        logits = ensemble(torch.from_numpy(spec).unsqueeze(0))
    inferred, clock = time.monotonic() - clock, time.monotonic()
    peaks.append(high_water())
    metrical = postprocess_metrical_structure(logits, ensemble.cfg)
    functional = postprocess_functional_structure(logits, ensemble.cfg)
    bpm = estimate_tempo_from_beats(metrical["beats"])
    log.info("song of %.0f s: separated in %.1f s, spectrograms %.1f s, allin1 %.1f s, downbeats and sections %.1f s, %d threads; "
             "peak MiB after each of the first three %s",
             len(stereo) / STEREO_RATE, separated, spectra, inferred, time.monotonic() - clock, torch.get_num_threads(), peaks)
    # Rounded as the eighth version keeps it (adapters/music.py, Allin1.structure).
    return {"bpm": bpm, "beats": [round(float(t), 3) for t in metrical["beats"]],
            "downbeats": [round(float(t), 3) for t in metrical["downbeats"]],
            "segments": [{"start": round(float(s.start), 2), "end": round(float(s.end), 2), "label": s.label}
                         for s in functional]}


def confirmed(conn):
    """Whether the caller says the streams it wrote are whole, in a frame of its own after them."""
    try:
        frame, _ = protocol.read_request(conn, 0)
    except (protocol.ProtocolError, ValueError, OSError):
        return False
    return frame.get("streams") == "complete"


def op_analyze(models, request, fds, conn):
    streams = request.get("streams")
    expected = [{"name": "stereo", "channels": 2, "rate": STEREO_RATE}, {"name": "mono", "channels": 1, "rate": MONO_RATE}]
    if streams != expected or len(fds) != 2:
        raise Refused("analyze reads a stereo stream at 44.1 kHz, then a mono one at 22050 Hz")
    started = time.monotonic()
    stereo = read_stream(fds[0], 2, rate=STEREO_RATE)
    mono = read_stream(fds[1], 1, rate=MONO_RATE)[:, 0]
    if request.get("confirm") and not confirmed(conn):
        raise Dropped("the streams were not confirmed whole")
    loudness = rms_curve(mono).astype("<f4")
    del mono
    found = structure(models, stereo)
    if not found["beats"]:
        raise Refused("allin1 finds no beat in the song")
    return {"model": models.identity, **found, "loudness": base64.b64encode(loudness.tobytes()).decode(),
            "seconds": round(time.monotonic() - started, 1)}


def answer(models, request, fds, conn):
    op = request.get("op")
    try:
        if op == "info":
            models.load()
            return protocol.ok(model=models.identity)
        if op == "analyze":
            return protocol.ok(**op_analyze(models, request, fds, conn))
    except Refused as refused:
        return protocol.failure(protocol.CODE_INVALID_INPUT, str(refused))
    return protocol.failure(protocol.CODE_INVALID_INPUT, f"unknown operation {op!r}")


def handle(models, conn):
    try:
        request, fds = protocol.read_request(conn, 2)
    except (protocol.ProtocolError, ValueError, OSError) as broken:
        log.warning("dropped a request: %s", broken)
        return
    try:
        response = answer(models, request, fds, conn)
    except Dropped as dropped:
        log.warning("dropped a request: %s", dropped)
        return
    except MemoryError:
        log.exception("out of memory analysing a song")
        response = protocol.failure(protocol.CODE_INTERNAL, "out of memory")
    except Exception:
        log.exception("operation %s failed", request.get("op"))
        response = protocol.failure(protocol.CODE_INTERNAL, "the operation failed")
    finally:
        for fd in fds:
            with contextlib.suppress(OSError):
                os.close(fd)
    with contextlib.suppress(OSError, protocol.ProtocolError):
        protocol.write_response(conn, response)


def listening_socket(path):
    if os.environ.get("LISTEN_PID") == str(os.getpid()) and os.environ.get("LISTEN_FDS") == "1":
        return socket.socket(fileno=SYSTEMD_FIRST_FD)
    if not path:
        raise SystemExit("--socket is required without systemd socket activation")
    with contextlib.suppress(FileNotFoundError):
        os.unlink(path)
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    listener.bind(path)
    listener.listen(4)
    return listener


def serve(listener, models, idle_seconds=IDLE_SECONDS):
    """Answers connections one at a time until none arrives for idle_seconds; systemd starts it again on the next."""
    listener.settimeout(idle_seconds)
    while True:
        try:
            conn, _ = listener.accept()
        except TimeoutError:
            log.info("idle for %d s; exiting", idle_seconds)
            return
        with conn:
            conn.settimeout(None)
            handle(models, conn)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--socket", help="listen here without systemd socket activation")
    parser.add_argument("--allin1", required=True, help="the folder of allin1's eight folds")
    parser.add_argument("--allin1-manifest", required=True)
    parser.add_argument("--demucs", required=True, help="the folder of htdemucs")
    parser.add_argument("--demucs-manifest", required=True)
    parser.add_argument("--threads", type=int, default=0, help="PyTorch's threads; 0 leaves its default")
    parser.add_argument("--idle-seconds", type=int, default=IDLE_SECONDS)
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format="%(levelname)s %(message)s")
    models = Models(args.allin1_manifest, args.allin1, args.demucs_manifest, args.demucs, args.threads)
    serve(listening_socket(args.socket), models, args.idle_seconds)


if __name__ == "__main__":
    main()
