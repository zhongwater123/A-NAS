"""The montage center's compute Worker (docs/architecture/montage-center.md, 剪辑计算 Worker).

It runs the prototype's pure computation (domain/) for the photo service,
which cuts cards and measures their quality, and for the montage center,
which lists the catalog, asks for plans and fills the music's slots. It has
no network and no data volume: everything it reads comes in the request.

Each connection carries one request and one answer, framed as the AI
Worker's are (anas_ai.protocol). A request names its arrays in "arrays",
each {"name", "dtype", "shape"}, and passes them in that order as memfd
descriptors; the Worker checks only their lengths. Answers are JSON.

Operations:
  cards         windows of one video: "windows" [{start, end}] in seconds,
                arrays vectors (float32, n × d) and flat (uint8, n); answers
                the cards with their keyframe times (domain.cards.plan_cards).
  quality_part  one part of a card's quality: "kind" shake (array frames,
                uint8 m × 180 × 320) answers the moves between neighbours;
                keyframe (array jpeg, the encoded keyframe) answers blocked
                and light; sharp (array frame, uint8 h × w) answers points.
  quality       a card's quality from its parts: "moves", "blurs",
                "blocked", "points"; answers blur, shake, blocked, points and
                the reasons it cannot be used (domain.quality.reasons).
  catalog       the cards that may be planned with (domain.catalog.usable).
  clear         where nothing covers the lens around a keyframe: arrays key
                (uint8 144 × 256), and before and after (uint8 k × 144 × 256)
                every 0.5 s from lo to key and from key to hi.
  fill          the timeline (domain.fill.fill). "known" holds the clear
                spans found so far, keyed as the prototype caches them; one
                still unknown answers "need" for the caller to measure
                (clear) and call again.
  plan          the request of a planning step: "step" pick or shots.
  shots_answer  the second step's answer, back to lists.
"""

import argparse
import contextlib
import io
import logging
import math
import mmap
import os
import socket
import statistics
import sys

import numpy as np

from anas_ai import protocol

from .domain import cards as cardrules
from .domain import catalog as catalogrules
from .domain import fill as fillrules
from .domain import picture
from .domain import plan as planrules
from .domain import quality as qualityrules
from .domain.options import Case, CutOptions

log = logging.getLogger("anas_montage")

SYSTEMD_FIRST_FD = 3
# The most arrays one request passes.
MAX_ARRAYS = 8
# The most bytes all arrays of a request hold: a 4K keyframe at 1920 wide is
# 4 MB; 30 s of shake frames 17 MB; a video's window vectors 33 MB.
MAX_ARRAY_BYTES = 64 << 20
DTYPES = {"float32": np.float32, "uint8": np.uint8}
# The prototype's thresholds for a keyframe dark enough to count its sharp
# points (pipeline.py, DARK), and the size the keyframe is shrunk to for
# blocking (picture.GRID).
DARK = 60


class Refused(Exception):
    """The request cannot be answered as it stands; the same request would fail again."""


def read_arrays(request, fds):
    """The request's arrays by name, read from their descriptors."""
    specs = request.get("arrays", [])
    if not isinstance(specs, list) or len(specs) != len(fds):
        raise Refused(f"{len(fds)} descriptors for {len(specs) if isinstance(specs, list) else 0} arrays")
    arrays, total = {}, 0
    for spec, fd in zip(specs, fds, strict=True):
        dtype = DTYPES.get(spec.get("dtype"))
        shape = spec.get("shape")
        if dtype is None or not isinstance(shape, list) or not all(isinstance(n, int) and n >= 0 for n in shape):
            raise Refused(f"array {spec.get('name')!r} is not described")
        size = math.prod(shape) * np.dtype(dtype).itemsize
        total += size
        if total > MAX_ARRAY_BYTES:
            raise Refused("the arrays are too large")
        length = os.fstat(fd).st_size
        if length != size:
            raise Refused(f"array {spec['name']!r} has {length} bytes, not {size}")
        if size == 0:
            arrays[spec["name"]] = np.zeros(shape, dtype)
            continue
        with mmap.mmap(fd, size, prot=mmap.PROT_READ) as view:
            arrays[spec["name"]] = np.frombuffer(view, dtype).reshape(shape).copy()
    return arrays


def grey_image(jpeg, size=picture.GRID):
    """The keyframe as the prototype reads it: grey, at size (store.grey_image)."""
    from PIL import Image

    with Image.open(io.BytesIO(jpeg)) as image:
        return np.asarray(image.convert("L").resize(size), dtype=np.float32)


def mean_light(jpeg):
    from PIL import Image

    with Image.open(io.BytesIO(jpeg)) as image:
        return float(np.asarray(image.convert("L"), dtype=np.float32).mean())


def moves(frames):
    """The first half of picture.jitter: each neighbouring pair's shift, under its window."""
    f = frames.astype(np.float32)
    if len(f) < 2:
        return []
    h, w = f.shape[1:]
    window = np.hanning(h)[:, None] * np.hanning(w)[None, :]
    return [list(picture.shift(f[i] * window, f[i + 1] * window)) for i in range(len(f) - 1)]


def shake(pairs):
    """The second half of picture.jitter, over a whole card's moves."""
    if len(pairs) + 1 < picture.SMOOTH + 2:
        return 0.0
    moved = np.array(pairs, dtype=np.float64)
    kernel = np.ones(picture.SMOOTH) / picture.SMOOTH
    smooth = np.stack([np.convolve(moved[:, k], kernel, mode="same") for k in range(2)], axis=1)
    half = picture.SMOOTH // 2
    return round(float(np.median(np.hypot(*(moved - smooth)[half:-half].T))), 3)


def op_cards(request, arrays):
    vectors, flat = arrays["vectors"], arrays["flat"]
    windows = request["windows"]
    if len(windows) != len(vectors) or len(flat) != len(vectors) or not windows:
        raise Refused("windows, vectors and flat differ in number")
    rows = [("v", float(w["start"]), float(w["end"])) for w in windows]
    index = cardrules.WindowIndex(videos={"v": {}}, rows=rows, frames=vectors.astype(np.float32), flat=flat.astype(bool))
    planned = cardrules.plan_cards(index, request.get("key", "card"))
    return {"cards": [{k: c[k] for k in ("id", "start", "end", "windows", "times", "keys")} for c in planned]}


def op_quality_part(request, arrays):
    kind = request.get("kind")
    if kind == "shake":
        frames = arrays["frames"]
        if frames.ndim != 3 or frames.shape[1:] != (picture.SHAKE_SIZE[1], picture.SHAKE_SIZE[0]):
            raise Refused("shake frames are not 320 × 180")
        return {"moves": moves(frames)}
    if kind == "keyframe":
        jpeg = arrays["jpeg"].tobytes()
        return {"blocked": picture.blocked(grey_image(jpeg)), "light": round(mean_light(jpeg), 3)}
    if kind == "sharp":
        frame = arrays["frame"]
        if frame.ndim != 2:
            raise Refused("a sharp frame is one grey picture")
        return {"points": picture.sharp_points(frame.astype(np.float32))}
    raise Refused(f"unknown quality part {kind!r}")


def op_quality(request, _arrays):
    blurs = [float("nan") if b is None else float(b) for b in request["blurs"]]
    blocked = [float(b) for b in request["blocked"]]
    points = request["points"]
    if not blurs or len(blocked) != len(blurs) or len(points) != len(blurs):
        raise Refused("a card's keyframes differ in number")
    q = {"blur": round(float(statistics.median(blurs)), 3), "shake": shake(request["moves"]),
         "blocked": float(statistics.median(blocked)), "points": points}
    reasons = qualityrules.reasons(q)
    return {**q, "blur": None if math.isnan(q["blur"]) else q["blur"], "reasons": reasons}


def op_catalog(request, _arrays):
    described = request["cards"]
    usable = catalogrules.usable(described, request.get("measures", {}), set(request.get("kept", [])),
                                 request.get("excluded", []), quality=request.get("quality", True))
    return {"cards": [c["id"] for c in usable]}


def op_clear(request, arrays):
    key, lo, hi = float(request["key"]), float(request["lo"]), float(request["hi"])
    pictures = {None: arrays["key"][None, :, :].astype(np.float32), "after": arrays["after"].astype(np.float32),
                "before": arrays["before"].astype(np.float32)}

    def frames(start, end):
        if end is None:
            return pictures[None]
        return pictures["after"] if start == key else pictures["before"]

    return {"span": list(picture.clear(frames, key, lo, hi))}


class Need(Exception):
    """fill needs a clear span it was not given."""

    def __init__(self, need):
        super().__init__(need["name"])
        self.need = need


def op_fill(request, arrays):
    vectors = arrays["vectors"].astype(np.float32)
    rows = [(r["video"], float(r["start"]), float(r["end"])) for r in request["rows"]]
    if len(rows) != len(vectors):
        raise Refused("rows and vectors differ in number")
    index = cardrules.WindowIndex(videos={}, rows=rows, frames=vectors, flat=np.zeros(len(rows), bool))
    known = request.get("known", {})
    turned = set(request.get("turned", []))
    footage = fillrules.Footage(cards=request["cards"], catalog=request["catalog"], index=index,
                                source=lambda video: video, turned=turned, parts=request.get("parts", {}))

    def held(beat, file, key, lo, hi, turned_=False):
        name = f"{beat}@{lo:.2f}-{hi:.2f}"
        if name not in known:
            raise Need({"name": name, "beat": beat, "video": file, "key": key, "lo": lo, "hi": hi, "turned": bool(turned_)})
        return tuple(known[name])

    try:
        clips, short = fillrules.fill(request["music"], request["planned"], request["picked"], footage, held,
                                      fps=int(request["fps"]), labels=request.get("labels") or {})
    except Need as need:
        return {"need": need.need}
    return {"clips": clips, "short": [list(s) for s in short]}


def options(request):
    return Case(**request["case"]), CutOptions(**request.get("options", {}))


def op_plan(request, _arrays):
    case, opts = options(request)
    if request.get("step") == "pick":
        system, user, schema = planrules.pick_request(case, opts, request["cards"], request["music"])
        return {"system": system, "user": user, "schema": schema}
    if request.get("step") == "shots":
        return planrules.shots_request(case, opts, request["picked"], request["cards"], request["music"])
    raise Refused("step is pick or shots")


def op_shots_answer(request, _arrays):
    raw, answer = planrules.shots_answer(request["answer"], request["pictures"], request["picked"])
    return {"raw": raw, "answer": answer}


OPS = {"cards": op_cards, "quality_part": op_quality_part, "quality": op_quality, "catalog": op_catalog,
       "clear": op_clear, "fill": op_fill, "plan": op_plan, "shots_answer": op_shots_answer}


def answer(request, fds):
    op = OPS.get(request.get("op"))
    if op is None:
        return protocol.failure(protocol.CODE_INVALID_INPUT, f"unknown operation {request.get('op')!r}")
    try:
        return protocol.ok(**op(request, read_arrays(request, fds)))
    except Refused as refused:
        return protocol.failure(protocol.CODE_INVALID_INPUT, str(refused))
    except (KeyError, TypeError, ValueError, IndexError) as broken:
        return protocol.failure(protocol.CODE_INVALID_INPUT, f"{type(broken).__name__}: {broken}")


def handle(conn):
    try:
        request, fds = protocol.read_request(conn, MAX_ARRAYS)
    except (protocol.ProtocolError, ValueError, OSError) as broken:
        log.warning("dropped a request: %s", broken)
        return
    try:
        response = answer(request, fds)
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
    listener.listen(8)
    return listener


def serve(listener):
    """Answers connections one at a time, for as long as it runs: a single instance, so NumPy loads once."""
    while True:
        conn, _ = listener.accept()
        with conn:
            handle(conn)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--socket", help="listen here without systemd socket activation")
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, stream=sys.stderr, format="%(levelname)s %(message)s")
    serve(listening_socket(args.socket))


if __name__ == "__main__":
    main()
