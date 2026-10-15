"""AI providers: image vectors through MediaPipe, object detection through
ONNX Runtime, faces through OpenCV (and ONNX Runtime for an ArcFace-style
recognition model), and deterministic fakes of each.

Only the real providers import their runtimes (and NumPy and Pillow), so the
Worker and its tests run with the Python standard library alone when the
fakes are used.
"""

import base64
import gc
import hashlib
import importlib.metadata
import io
import json
import math
import os
import struct
import time


class InvalidInput(Exception):
    """The provider cannot process this input; the same input fails again."""


class Unavailable(Exception):
    """The provider cannot serve at all, for example without its model."""


class Cancelled(Exception):
    """The caller hung up, so the work was stopped before it finished."""


# AI works in idle time with what the device has, but leaves these cores to
# the photo service and the user, so that a request arriving mid-inference is
# served at once (docs/architecture/photo-objects.md#资源策略).
RESERVED_CORES = 2


def inference_threads():
    """The cores this process may run on, less the reserve, and at least one."""
    return max(1, len(os.sched_getaffinity(0)) - RESERVED_CORES)


def verify_model(model_path, manifest_path):
    """Returns the manifest when model_path is exactly the file it pins."""
    with open(manifest_path, encoding="utf-8") as file:
        manifest = json.load(file)
    try:
        size = os.stat(model_path).st_size
    except OSError as error:
        raise Unavailable(f"model file {model_path}: {error.strerror}") from error
    if size != manifest["sizeBytes"]:
        raise Unavailable(f"model file {model_path} has {size} bytes, the manifest pins {manifest['sizeBytes']}")
    digest = hashlib.sha256()
    with open(model_path, "rb") as file:
        for chunk in iter(lambda: file.read(1 << 20), b""):
            digest.update(chunk)
    if digest.hexdigest() != manifest["sha256"]:
        raise Unavailable(f"model file {model_path} does not match the manifest's SHA-256")
    return manifest


# The LiteRT package carries image signatures of 70 and 140 soft tokens.
# 70 is about three times faster on a 4-core CPU with similar zero-shot
# results in the first trial; the benchmark decides between them.
VISION_TOKENS = (70, 140)


class MediaPipeProvider:
    """EmbeddingGemma 2 through MediaPipe's Universal Embedder."""

    # EmbeddingGemma's retrieval prompt; images get none. The label
    # calibration (docs/research/photo-ai-label-calibration.md) found it the
    # better of the prompts tried for Chinese search.
    QUERY_PROMPT = "task: search result | query: {}"

    def __init__(self, model_path, manifest_path, vision_tokens=70, cache_dir=None):
        if vision_tokens not in VISION_TOKENS:
            raise ValueError(f"vision tokens must be one of {VISION_TOKENS}")
        manifest = verify_model(model_path, manifest_path)
        try:
            import mediapipe
            from mediapipe.tasks.python.core.base_options import BaseOptions
            from mediapipe.tasks.python.retrieval.universal_embedder import (
                UniversalEmbedder,
                UniversalEmbedderOptions,
            )
        except ImportError as error:
            # A runtime that cannot load MediaPipe is a broken installation,
            # not a reason to crash: under socket activation every connection
            # would restart the Worker until systemd gives up on the socket.
            raise Unavailable(f"the AI runtime cannot load MediaPipe: {error}") from error

        self._embedder = UniversalEmbedder.create_from_options(
            UniversalEmbedderOptions(
                base_options=BaseOptions(model_asset_path=model_path),
                l2_normalize=True,
                vision_tokens_per_image=vision_tokens,
                # XNNPack keeps repacked weights here; by default it tries the
                # model's own directory, which is read-only in deployment.
                cache_dir=cache_dir,
            )
        )
        self.dimensions = len(self._vector(self._embedder.embed_text("probe")))
        # The runtime and the token budget change the vectors, so they are
        # part of the model the vectors came from.
        self.model = (
            f"{manifest['name']}@{manifest['sha256'][:12]}"
            f"+mediapipe-{mediapipe.__version__}+tok{vision_tokens}+l2"
        )

    def embed_image(self, data):
        try:
            result = self._embedder.embed_image(data)
        except (RuntimeError, ValueError) as error:
            raise InvalidInput(str(error)) from error
        return self._vector(result)

    def embed_text(self, text):
        return self._vector(self._embedder.embed_text(text))

    def embed_query(self, text):
        return self.embed_text(self.QUERY_PROMPT.format(text))

    def close(self):
        self._embedder.close()

    @staticmethod
    def _vector(result):
        return [float(value) for value in result.embeddings[0].embedding]


class FakeProvider:
    """A deterministic stand-in: equal images get equal unit vectors."""

    def __init__(self, dimensions=768):
        self.dimensions = dimensions
        self.model = f"fake-sha256-{dimensions}"

    def embed_image(self, data):
        if not data:
            raise InvalidInput("empty image")
        return self._vector(data)

    def embed_query(self, text):
        return self._vector(text.encode())

    def _vector(self, data):
        values = []
        counter = 0
        while len(values) < self.dimensions:
            block = hashlib.sha256(counter.to_bytes(4, "big") + data).digest()
            values.extend(byte / 255 - 0.5 for byte in block)
            counter += 1
        values = values[: self.dimensions]
        norm = math.sqrt(sum(value * value for value in values)) or 1.0
        return [value / norm for value in values]

    def close(self):
        pass


def encode_vector(vector):
    """Little-endian float32, as the protocol carries vectors."""
    return struct.pack(f"<{len(vector)}f", *vector)


# The detector's square input. Like the evaluation
# (docs/research/photo-ai-object-detection.md), the photo is resized to it
# without keeping its aspect ratio, and the boxes above MIN_BOX_SCORE are kept,
# at most MAX_BOXES, so that search can weigh every score.
DETECTOR_INPUT = 640
MIN_BOX_SCORE = 0.05
MAX_BOXES = 100


class RunHandle:
    """Lets the Worker stop a detection from another thread."""

    terminate = False


class OnnxDetector:
    """D-FINE (Objects365) exported with its post-processing, through ONNX Runtime.

    The export takes "images" (N x 3 x 640 x 640, RGB in [0, 1]) and
    "orig_target_sizes" (N x 2) and returns "labels", "boxes" (corners scaled
    to the sizes) and "scores" for its 300 queries. The session is created on
    the first detection and dropped by release(), so the model's memory is held
    only while there is work.
    """

    def __init__(self, model_path, manifest_path, threads=None):
        manifest = verify_model(model_path, manifest_path)
        try:
            version = importlib.metadata.version("onnxruntime")
        except importlib.metadata.PackageNotFoundError as error:
            raise Unavailable("the AI runtime has no ONNX Runtime") from error
        self._path = model_path
        self._session = None
        self.threads = threads or inference_threads()
        # The runtime, the input size and what is kept change the boxes, so
        # they are part of the model the boxes came from.
        self.model = (
            f"{manifest['name']}@{manifest['sha256'][:12]}"
            f"+ort-{version}+{DETECTOR_INPUT}+s{MIN_BOX_SCORE:g}k{MAX_BOXES}"
        )

    def run_options(self):
        import onnxruntime

        return onnxruntime.RunOptions()

    def detect(self, data, run):
        import numpy

        pixels = detector_pixels(data)
        size = numpy.array([[DETECTOR_INPUT, DETECTOR_INPUT]], dtype=numpy.int64)
        try:
            labels, boxes, scores = self._load().run(["labels", "boxes", "scores"],
                                                     {"images": pixels, "orig_target_sizes": size}, run)
        except Exception:  # pylint: disable=broad-except
            # A terminated run fails with ONNX Runtime's generic error.
            if not run.terminate:
                raise
            labels = None
        # Raised outside the handler, so no traceback keeps this frame, or the
        # session it ran, alive after a release.
        if labels is None:
            raise Cancelled()
        return kept_boxes(labels[0].tolist(), (boxes[0] / DETECTOR_INPUT).tolist(), scores[0].tolist())

    def release(self):
        """Drops the session and its threads. A session still referenced from
        a reference cycle, such as a stored exception, would otherwise linger
        until the cyclic collector happens to run, and each reload would add
        another (seen on the local stack: about 11 threads and 150 MB a time)."""
        self._session = None
        gc.collect()

    def _load(self):
        if self._session is None:
            try:
                import onnxruntime
            except ImportError as error:
                raise Unavailable(f"the AI runtime cannot load ONNX Runtime: {error}") from error
            options = onnxruntime.SessionOptions()
            options.intra_op_num_threads = self.threads
            options.inter_op_num_threads = 1
            # Idle threads sleep instead of spinning. Spinning held every
            # thread's core between operators: beside a kiosk-like load on
            # eight cores it tripled the CPU each photo took and the load's
            # waiting for a core, and slowed detection itself
            # (docs/investigations/2026-10-13-idle-detection-used-one-core.md).
            options.add_session_config_entry("session.intra_op.allow_spinning", "0")
            self._session = onnxruntime.InferenceSession(self._path, options, providers=["CPUExecutionProvider"])
        return self._session


def detector_pixels(data):
    """The detector's input from the photo service's JPEG."""
    import numpy
    from PIL import Image, ImageOps, UnidentifiedImageError

    try:
        with Image.open(io.BytesIO(data)) as opened:
            image = ImageOps.exif_transpose(opened).convert("RGB")
            resized = image.resize((DETECTOR_INPUT, DETECTOR_INPUT), Image.Resampling.BILINEAR)
    except (UnidentifiedImageError, OSError, ValueError) as error:
        raise InvalidInput(f"not an image: {error}") from error
    return (numpy.asarray(resized, dtype=numpy.float32) / 255.0).transpose(2, 0, 1)[None]


def kept_boxes(labels, boxes, scores):
    """[class, score, x0, y0, x1, y1] above MIN_BOX_SCORE, best first, corners in [0, 1]."""
    found = []
    for label, box, score in zip(labels, boxes, scores):
        if score >= MIN_BOX_SCORE:
            corners = [round(min(1.0, max(0.0, value)), 4) for value in box]
            found.append([int(label), round(float(score), 4), *corners])
    found.sort(key=lambda box: -box[1])
    return found[:MAX_BOXES]


class FakeDetector:
    """A deterministic stand-in: equal images get equal boxes.

    delay keeps a detection running for that long, checking whether it was
    told to stop, so that tests can cancel one.
    """

    model = "fake-detector-sha256"

    def __init__(self, delay=0.0):
        self.delay = delay
        self.loaded = False
        self.cancelled = 0

    def run_options(self):
        return RunHandle()

    def detect(self, data, run):
        if not data:
            raise InvalidInput("empty image")
        self.loaded = True
        deadline = time.monotonic() + self.delay
        while time.monotonic() < deadline:
            if run.terminate:
                self.cancelled += 1
                raise Cancelled()
            time.sleep(0.01)
        digest = hashlib.sha256(data).digest()
        labels, boxes, scores = [], [], []
        for i in range(digest[0] % 4):
            part = digest[1 + 6 * i: 7 + 6 * i]
            x0, y0 = part[2] / 510, part[3] / 510
            labels.append(1 + part[0] % 365)
            scores.append(0.3 + part[1] / 255 * 0.7)
            boxes.append([x0, y0, x0 + 0.5, y0 + 0.5])
        return kept_boxes(labels, boxes, scores)

    def release(self):
        self.loaded = False


# Faces (docs/architecture/photo-people.md): YuNet finds each face and its
# five landmarks in the photo service's 1600 px input, the landmarks align it
# to ArcFace's 112 x 112 template, and the recognition model embeds it. The
# score floor and the largest number of faces kept come from the people
# evaluation.
FACE_MIN_SCORE = 0.8
FACE_NMS = 0.3
MAX_FACES = 64
ALIGNED_FACE = 112
# The five points ArcFace-style models are trained on, in the aligned crop:
# the eyes, the nose tip and the mouth corners; OpenCV's alignCrop uses them too.
FACE_TEMPLATE = ((38.2946, 51.6963), (73.5318, 51.5014), (56.0252, 71.7366), (41.5493, 92.3655), (70.7299, 92.2041))
# Recognition models the Worker knows how to feed, by the manifest's "kind":
# SFace through OpenCV, or an ArcFace-style ONNX model through ONNX Runtime.
FACE_KINDS = ("sface", "arcface")


def similarity_transform(source, target):
    """The 2 x 3 rotation, uniform scale and shift that best maps source onto target (Umeyama)."""
    import numpy

    source = numpy.asarray(source, dtype=numpy.float64)
    target = numpy.asarray(target, dtype=numpy.float64)
    source_mean, target_mean = source.mean(axis=0), target.mean(axis=0)
    source_centred, target_centred = source - source_mean, target - target_mean
    u, s, vt = numpy.linalg.svd(target_centred.T @ source_centred / len(source))
    signs = numpy.ones(2)
    if numpy.linalg.det(u) * numpy.linalg.det(vt) < 0:
        signs[-1] = -1
    rotation = u @ numpy.diag(signs) @ vt
    scale = (s * signs).sum() / source_centred.var(axis=0).sum()
    shift = target_mean - scale * rotation @ source_mean
    return numpy.hstack([scale * rotation, shift[:, None]])


def align_face(image, landmarks):
    """The 112 x 112 crop of a face, its landmarks moved onto FACE_TEMPLATE."""
    import cv2

    matrix = similarity_transform(landmarks, FACE_TEMPLATE)
    return cv2.warpAffine(image, matrix, (ALIGNED_FACE, ALIGNED_FACE), flags=cv2.INTER_LINEAR)


class FaceProvider:
    """YuNet and a recognition model, loaded on the first photo and dropped by release()."""

    def __init__(self, detector_path, detector_manifest, embedder_path, embedder_manifest, threads=None):
        detector = verify_model(detector_path, detector_manifest)
        embedder = verify_model(embedder_path, embedder_manifest)
        self.kind = embedder.get("kind")
        if self.kind not in FACE_KINDS:
            raise Unavailable(f"face model kind {self.kind!r} is not one of {FACE_KINDS}")
        try:
            import cv2
        except ImportError as error:
            raise Unavailable(f"the AI runtime cannot load OpenCV: {error}") from error
        runtime = f"opencv-{cv2.__version__}"
        if self.kind == "arcface":
            try:
                runtime += "+ort-" + importlib.metadata.version("onnxruntime")
            except importlib.metadata.PackageNotFoundError as error:
                raise Unavailable("the AI runtime has no ONNX Runtime") from error
        self._detector_path, self._embedder_path = detector_path, embedder_path
        self._detector = self._embedder = None
        self.threads = threads or inference_threads()
        self.model = (
            f"{detector['name']}@{detector['sha256'][:12]}+{embedder['name']}@{embedder['sha256'][:12]}"
            f"+{runtime}+s{FACE_MIN_SCORE:g}k{MAX_FACES}"
        )

    def run_options(self):
        if self.kind == "arcface":
            import onnxruntime

            return onnxruntime.RunOptions()
        return RunHandle()

    def detect(self, data, run):
        import cv2
        import numpy

        image = cv2.imdecode(numpy.frombuffer(data, numpy.uint8), cv2.IMREAD_COLOR)
        if image is None:
            raise InvalidInput("not an image")
        height, width = image.shape[:2]
        detector = self._load_detector()
        detector.setInputSize((width, height))
        _, found = detector.detect(image)
        if run.terminate:
            raise Cancelled()
        rows = [] if found is None else [row for row in found.tolist() if row[14] >= FACE_MIN_SCORE]
        rows.sort(key=lambda row: -row[14])
        rows = rows[:MAX_FACES]
        crops = [align_face(image, numpy.array(row[4:14]).reshape(5, 2)) for row in rows]
        vectors = self._embed(crops, run) if crops else []
        faces = []
        for row, vector in zip(rows, vectors):
            x, y, w, h = row[:4]
            box = [x / width, y / height, (x + w) / width, (y + h) / height]
            landmarks = [value / (width if i % 2 == 0 else height) for i, value in enumerate(row[4:14])]
            faces.append({
                "box": [round(min(1.0, max(0.0, value)), 5) for value in box],
                "landmarks": [round(min(1.0, max(0.0, value)), 5) for value in landmarks],
                "score": round(float(row[14]), 4),
                "vector": encode_unit(vector),
            })
        return faces

    def _embed(self, crops, run):
        import cv2
        import numpy

        if self.kind == "sface":
            recognizer = self._load_embedder()
            vectors = []
            for crop in crops:
                if run.terminate:
                    raise Cancelled()
                vectors.append(recognizer.feature(crop).reshape(-1))
            vectors = numpy.stack(vectors)
        else:
            session = self._load_embedder()
            blob = cv2.dnn.blobFromImages(crops, 1 / 127.5, (ALIGNED_FACE, ALIGNED_FACE), (127.5, 127.5, 127.5),
                                          swapRB=True)
            try:
                vectors = session.run(None, {session.get_inputs()[0].name: blob}, run)[0]
            except Exception:  # pylint: disable=broad-except
                # A terminated run fails with ONNX Runtime's generic error.
                if not run.terminate:
                    raise
                vectors = None
            if vectors is None:
                raise Cancelled()
        return vectors / numpy.linalg.norm(vectors, axis=1, keepdims=True)

    def release(self):
        self._detector = self._embedder = None
        gc.collect()

    def _load_detector(self):
        if self._detector is None:
            import cv2

            cv2.setNumThreads(self.threads)
            self._detector = cv2.FaceDetectorYN.create(self._detector_path, "", (320, 320), FACE_MIN_SCORE,
                                                       FACE_NMS, 5000)
        return self._detector

    def _load_embedder(self):
        if self._embedder is None:
            if self.kind == "sface":
                import cv2

                self._embedder = cv2.FaceRecognizerSF.create(self._embedder_path, "")
            else:
                import onnxruntime

                options = onnxruntime.SessionOptions()
                options.intra_op_num_threads = self.threads
                options.inter_op_num_threads = 1
                options.add_session_config_entry("session.intra_op.allow_spinning", "0")
                # AuraFace's export declares a batch of one for its output and
                # warns on every larger batch, which it runs correctly.
                options.log_severity_level = 3
                self._embedder = onnxruntime.InferenceSession(self._embedder_path, options,
                                                              providers=["CPUExecutionProvider"])
        return self._embedder


def encode_unit(vector):
    """A vector as base64 little-endian float32, as detect_faces carries it."""
    return base64.b64encode(encode_vector([float(value) for value in vector])).decode("ascii")


class FakeFaces:
    """A deterministic stand-in: equal images get equal faces.

    Each face belongs to one of FAKE_PEOPLE people chosen by the image bytes,
    and the same person gets nearly the same vector in every photo, so the
    photo service's clustering has something to cluster in system tests.
    """

    model = "fake-faces-sha256"
    dimensions = 128
    FAKE_PEOPLE = 3

    def __init__(self):
        self.loaded = False

    def run_options(self):
        return RunHandle()

    def detect(self, data, run):
        if not data:
            raise InvalidInput("empty image")
        self.loaded = True
        digest = hashlib.sha256(data).digest()
        faces = []
        for i in range(digest[0] % 3):
            person = digest[1 + i] % self.FAKE_PEOPLE
            base = FakeProvider(self.dimensions)._vector(b"fake person %d" % person)
            noise = FakeProvider(self.dimensions)._vector(data + bytes([i]))
            vector = [b + 0.05 * n for b, n in zip(base, noise)]
            norm = math.sqrt(sum(value * value for value in vector))
            x0 = 0.05 + 0.3 * i
            faces.append({
                "box": [x0, 0.2, x0 + 0.2, 0.5],
                "landmarks": [x0 + 0.06, 0.3, x0 + 0.14, 0.3, x0 + 0.1, 0.36, x0 + 0.07, 0.42, x0 + 0.13, 0.42],
                "score": round(0.9 - 0.01 * i, 4),
                "vector": encode_unit(value / norm for value in vector),
            })
        return faces

    def release(self):
        self.loaded = False
