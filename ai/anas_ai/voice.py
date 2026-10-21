"""Runs the voice Worker (docs/architecture/voice.md).

    python3 -m anas_ai.voice --speaker-model FILE --speaker-manifest FILE [--socket PATH]
    python3 -m anas_ai.voice --fake [--socket PATH]

The Product Service sends speech as one descriptor of 16 kHz mono 16-bit
little-endian samples, decoded by the media Worker, and names the stretches
of it to use by sample offset. The Worker answers with speaker vectors, so
voiceprints are computed on the device and never leave it (ADR 0027). It has
no network and no data volume; the framing is the photo AI Worker's
(anas_ai.protocol).

    info            -> model, dimensions, sampleRate
    embed_speakers  groups: [[[offset, count], ...], ...]
                    -> one L2-normalised vector per group: the mean of its
                       segments' vectors, weighted by their length
    cluster_speech  segments: [[offset, count], ...], threshold
                    -> labels: one per segment, and centroids: one vector
                       per label, for recordings the recognition service
                       did not tell speakers apart in
    release         what: "all" -> exit after answering

Vectors travel as base64 little-endian float32. Like the AI Worker, it serves
one connection at a time and exits after IDLE_SECONDS without one.
"""

import argparse
import base64
import hashlib
import logging
import math
import os
import struct
import sys

from anas_ai import protocol, providers

IDLE_SECONDS = 300
SAMPLE_RATE = 16000
# A request carries at most 30 minutes of samples; the Product Service splits
# longer work. 2 bytes a sample.
MAX_SAMPLES = SAMPLE_RATE * 60 * 30
MAX_PCM_BYTES = 2 * MAX_SAMPLES
# The model needs about half a second; a minute is more than one stretch of
# one voice needs.
MIN_SEGMENT = SAMPLE_RATE // 2
MAX_SEGMENT = SAMPLE_RATE * 60
MAX_SEGMENTS = 4000
MAX_GROUPS = 64
SPEAKER_KINDS = ("3d-speaker",)

OP_INFO = "info"
OP_EMBED_SPEAKERS = "embed_speakers"
OP_CLUSTER_SPEECH = "cluster_speech"
OP_RELEASE = "release"

log = logging.getLogger("anas-voice")


class SpeakerProvider:
    """A 3D-Speaker model through sherpa-onnx's speaker embedding extractor."""

    def __init__(self, model_path, manifest_path, threads=None):
        manifest = providers.verify_model(model_path, manifest_path)
        if manifest.get("kind") not in SPEAKER_KINDS:
            raise providers.Unavailable(f"speaker model kind {manifest.get('kind')!r} is not one of {SPEAKER_KINDS}")
        try:
            import numpy  # noqa: F401
            import sherpa_onnx
        except ImportError as error:
            raise providers.Unavailable(f"the voice runtime cannot load sherpa-onnx: {error}") from error
        config = sherpa_onnx.SpeakerEmbeddingExtractorConfig(
            model=model_path, num_threads=threads or providers.inference_threads(), provider="cpu")
        if not config.validate():
            raise providers.Unavailable(f"sherpa-onnx rejects the speaker model {model_path}")
        self._sherpa = sherpa_onnx
        self._extractor = sherpa_onnx.SpeakerEmbeddingExtractor(config)
        self.dimensions = self._extractor.dim
        self.model = f"{manifest['name']}@{manifest['sha256'][:12]}+sherpa-onnx-{sherpa_onnx.__version__}"

    def embed(self, samples):
        """The unit vector of one stretch of int16 samples."""
        import numpy

        stream = self._extractor.create_stream()
        stream.accept_waveform(SAMPLE_RATE, samples.astype(numpy.float32) / 32768)
        stream.input_finished()
        if not self._extractor.is_ready(stream):
            raise providers.InvalidInput("a segment is too short for the speaker model")
        vector = numpy.asarray(self._extractor.compute(stream), dtype=numpy.float32)
        norm = float(numpy.linalg.norm(vector))
        if not math.isfinite(norm) or norm == 0:
            raise providers.InvalidInput("a segment holds no voice")
        return vector / norm

    def cluster(self, vectors, threshold):
        """Labels for the unit vectors: sherpa-onnx's fast clustering on cosine distance."""
        import numpy

        if len(vectors) == 1:
            return [0]
        clustering = self._sherpa.FastClustering(self._sherpa.FastClusteringConfig(num_clusters=-1, threshold=threshold))
        return [int(label) for label in clustering(numpy.stack(vectors))]


class FakeSpeakers:
    """A deterministic stand-in: the vector of a stretch follows its loudest
    sample's sign and the stretch's length class, so tests can build voices."""

    model = "fake-speakers-sha256"
    dimensions = 32

    def embed(self, samples):
        data = bytes(samples) if not hasattr(samples, "tobytes") else samples.tobytes()
        if not data:
            raise providers.InvalidInput("empty segment")
        # The first sample decides the voice: tests fill a voice's stretches
        # with one constant value.
        (first,) = struct.unpack("<h", data[:2])
        base = providers.FakeProvider(self.dimensions)._vector(b"fake voice %d" % first)
        noise = providers.FakeProvider(self.dimensions)._vector(hashlib.sha256(data).digest())
        vector = [b + 0.05 * n for b, n in zip(base, noise)]
        norm = math.sqrt(sum(value * value for value in vector))
        return [value / norm for value in vector]

    def cluster(self, vectors, threshold):
        labels, centroids = [], []
        for vector in vectors:
            for label, centroid in enumerate(centroids):
                if 1 - sum(a * b for a, b in zip(vector, centroid)) < threshold:
                    labels.append(label)
                    break
            else:
                labels.append(len(centroids))
                centroids.append(vector)
        return labels


def listening_socket(path):
    import socket

    if os.environ.get("LISTEN_PID") == str(os.getpid()) and os.environ.get("LISTEN_FDS") == "1":
        return socket.socket(fileno=3)
    if not path:
        raise SystemExit("--socket is required without systemd socket activation")
    try:
        os.unlink(path)
    except FileNotFoundError:
        pass
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    listener.bind(path)
    listener.listen(8)
    return listener


def serve(listener, provider, unavailable_reason=None, idle_seconds=IDLE_SECONDS):
    """Answers connections until none arrives for idle_seconds or one asks it to release everything."""
    listener.settimeout(idle_seconds)
    while True:
        try:
            conn, _ = listener.accept()
        except TimeoutError:
            log.info("idle for %d s; exiting", idle_seconds)
            return
        with conn:
            conn.settimeout(None)
            if handle(conn, provider, unavailable_reason):
                log.info("released everything; exiting")
                return


def handle(conn, provider, unavailable_reason):
    """Answers one request; returns whether the Worker should exit."""
    try:
        request, fds = protocol.read_request(conn)
    except (protocol.ProtocolError, OSError, ValueError) as error:
        log.warning("dropped a malformed request: %s", error)
        return False
    try:
        response = respond(request, fds, provider, unavailable_reason)
    finally:
        for fd in fds:
            os.close(fd)
    try:
        protocol.write_response(conn, response)
    except OSError as error:
        log.warning("could not answer: %s", error)
    return request.get("op") == OP_RELEASE and response["ok"]


def respond(request, fds, provider, unavailable_reason):
    op = request.get("op")
    if op == OP_RELEASE:
        if request.get("what") != "all":
            return protocol.failure(protocol.CODE_INVALID_INPUT, 'release needs "what": "all"')
        return protocol.ok()
    if unavailable_reason is not None:
        return protocol.failure(protocol.CODE_UNAVAILABLE, unavailable_reason)
    if op == OP_INFO:
        return protocol.ok(model=provider.model, dimensions=provider.dimensions, sampleRate=SAMPLE_RATE)
    if op not in (OP_EMBED_SPEAKERS, OP_CLUSTER_SPEECH):
        return protocol.failure(protocol.CODE_INTERNAL, f"unknown operation {op!r}")
    try:
        if len(fds) != 1:
            raise providers.InvalidInput(f"{op} needs exactly one descriptor of samples")
        samples = read_samples(fds[0])
        if op == OP_EMBED_SPEAKERS:
            groups = request.get("groups")
            if not isinstance(groups, list) or not 1 <= len(groups) <= MAX_GROUPS:
                raise providers.InvalidInput(f"embed_speakers needs 1 to {MAX_GROUPS} groups")
            if sum(len(group) if isinstance(group, list) else 0 for group in groups) > MAX_SEGMENTS:
                raise providers.InvalidInput(f"at most {MAX_SEGMENTS} segments")
            vectors = [group_vector(provider, samples, segments_of(group, len(samples))) for group in groups]
            return protocol.ok(model=provider.model, vectors=[encode(vector) for vector in vectors])
        threshold = request.get("threshold")
        if not isinstance(threshold, (int, float)) or isinstance(threshold, bool) or not 0 < threshold < 2:
            raise providers.InvalidInput("cluster_speech needs a cosine distance threshold between 0 and 2")
        segments = segments_of(request.get("segments"), len(samples))
        vectors = [provider.embed(samples[start:start + count]) for start, count in segments]
        labels = provider.cluster(vectors, float(threshold))
        centroids = []
        for label in range(max(labels) + 1):
            members = [(vectors[i], segments[i][1]) for i, value in enumerate(labels) if value == label]
            centroids.append(encode(weighted_mean(members)))
        return protocol.ok(model=provider.model, labels=labels, centroids=centroids)
    except providers.InvalidInput as error:
        return protocol.failure(protocol.CODE_INVALID_INPUT, str(error))
    except providers.Unavailable as error:
        return protocol.failure(protocol.CODE_UNAVAILABLE, str(error))
    except Exception as error:  # pylint: disable=broad-except
        log.exception("%s failed", op)
        return protocol.failure(protocol.CODE_INTERNAL, type(error).__name__)


def read_samples(fd):
    """The descriptor's int16 samples, from its start."""
    os.lseek(fd, 0, os.SEEK_SET)
    chunks, size = [], 0
    while chunk := os.read(fd, 1 << 20):
        size += len(chunk)
        if size > MAX_PCM_BYTES:
            raise providers.InvalidInput(f"more than {MAX_SAMPLES // SAMPLE_RATE // 60} minutes of samples")
        chunks.append(chunk)
    data = b"".join(chunks)
    if len(data) % 2:
        raise providers.InvalidInput("samples are 16-bit")
    try:
        import numpy

        return numpy.frombuffer(data, dtype="<i2")
    except ImportError:
        return memoryview(data).cast("h")


def segments_of(value, total):
    """Validated [offset, count] pairs inside total samples."""
    if not isinstance(value, list) or not 1 <= len(value) <= MAX_SEGMENTS:
        raise providers.InvalidInput(f"1 to {MAX_SEGMENTS} segments are needed")
    segments = []
    for segment in value:
        if (not isinstance(segment, list) or len(segment) != 2
                or not all(isinstance(item, int) and not isinstance(item, bool) for item in segment)):
            raise providers.InvalidInput("a segment is [offset, count] in samples")
        start, count = segment
        if start < 0 or not MIN_SEGMENT <= count <= MAX_SEGMENT or start + count > total:
            raise providers.InvalidInput(f"a segment must lie inside the samples and last {MIN_SEGMENT} to {MAX_SEGMENT} samples")
        segments.append((start, count))
    return segments


def group_vector(provider, samples, segments):
    return weighted_mean([(provider.embed(samples[start:start + count]), count) for start, count in segments])


def weighted_mean(members):
    """The unit vector of the length-weighted mean of unit vectors."""
    dimensions = len(members[0][0])
    total = [0.0] * dimensions
    for vector, weight in members:
        for i in range(dimensions):
            total[i] += float(vector[i]) * weight
    norm = math.sqrt(sum(value * value for value in total))
    if norm == 0:
        raise providers.InvalidInput("the segments' voices cancel out")
    return [value / norm for value in total]


def encode(vector):
    return base64.b64encode(struct.pack(f"<{len(vector)}f", *vector)).decode("ascii")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--socket", help="listen here when not socket-activated")
    parser.add_argument("--speaker-model", help="the speaker embedding model's .onnx file")
    parser.add_argument("--speaker-manifest", help="the manifest that pins its size and SHA-256")
    parser.add_argument("--threads", type=int, help="inference threads (default: the cores less the reserve)")
    parser.add_argument("--fake", action="store_true", help="serve deterministic fake vectors")
    parser.add_argument("--idle-seconds", type=int, default=IDLE_SECONDS)
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s", stream=sys.stderr)
    provider, reason = None, None
    if args.fake:
        provider = FakeSpeakers()
    elif args.speaker_model and args.speaker_manifest:
        try:
            provider = SpeakerProvider(args.speaker_model, args.speaker_manifest, args.threads)
        except providers.Unavailable as error:
            # Answer "unavailable" instead of exiting, so socket activation
            # does not restart a Worker that cannot load its model.
            reason = str(error)
            log.error("speaker model unavailable: %s", reason)
    else:
        parser.error("give --fake, or --speaker-model and --speaker-manifest")
    if provider is not None:
        log.info("serving %s (%d dimensions)", provider.model, provider.dimensions)
    with listening_socket(args.socket) as listener:
        serve(listener, provider, reason, args.idle_seconds)
    logging.shutdown()
    os._exit(0)


if __name__ == "__main__":
    main()
