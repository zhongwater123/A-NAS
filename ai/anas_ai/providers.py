"""AI providers: image vectors through MediaPipe, object detection through
ONNX Runtime, faces through OpenCV (and ONNX Runtime for an ArcFace-style
recognition model), animal identity vectors through ONNX Runtime, the text in
photos through a llama-server child process, and deterministic fakes of each.

Only the real providers import their runtimes (and NumPy and Pillow), so the
Worker and its tests run with the Python standard library alone when the
fakes are used.
"""

import base64
import contextlib
import gc
import hashlib
import http.client
import importlib.metadata
import io
import json
import math
import os
import shutil
import socket
import struct
import subprocess
import tempfile
import threading
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


def read_manifest(manifest_path):
    """Returns a model's manifest, which must give its memoryBytes.

    memoryBytes is how much the Worker's memory, as its cgroup counts it,
    grows the first time the model runs, with the model file already in the
    page cache. The photo service's gate weighs it before background work
    loads the model (docs/architecture/photo-objects.md#资源策略).
    """
    try:
        with open(manifest_path, encoding="utf-8") as file:
            manifest = json.load(file)
    except (OSError, ValueError) as error:
        raise Unavailable(f"the manifest {manifest_path}: {error}") from error
    memory = manifest.get("memoryBytes")
    if type(memory) is not int or memory < 0:
        raise Unavailable(f"the manifest {manifest_path} gives no memoryBytes")
    return manifest


def verify_model(model_path, manifest_path):
    """Returns the manifest when model_path is exactly the file it pins."""
    manifest = read_manifest(manifest_path)
    verify_file(model_path, manifest)
    return manifest


def verify_file(model_path, manifest):
    """Raises Unavailable unless model_path has the manifest's size and SHA-256."""
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
        # The text probe has run; what the first image adds is still to come.
        self.memory = manifest["memoryBytes"]
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

    memory = 0

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
        self.memory = manifest["memoryBytes"]
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
    for label, box, score in zip(labels, boxes, scores, strict=True):
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
    memory = 0

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
        self.memory = detector["memoryBytes"] + embedder["memoryBytes"]
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
        for row, vector in zip(rows, vectors, strict=True):
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
    memory = 0
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
            vector = [b + 0.05 * n for b, n in zip(base, noise, strict=True)]
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


# Animals (docs/architecture/photo-people.md#宠物): a square around each box,
# ANIMAL_MARGIN times its longer side, resized to DINOv2's input, as the
# evaluation (ai/eval/pets.py) measured it.
ANIMAL_MARGIN = 1.1
ANIMAL_INPUT = 224
MAX_ANIMALS = 16
IMAGENET_MEAN = (0.485, 0.456, 0.406)
IMAGENET_STD = (0.229, 0.224, 0.225)
ANIMAL_KINDS = ("dinov2",)


def animal_crop(image, box):
    """The square around a box of fractions, ANIMAL_MARGIN times its longer side, inside the image."""
    height, width = image.shape[:2]
    x0, y0, x1, y1 = box[0] * width, box[1] * height, box[2] * width, box[3] * height
    side = max(1.0, min(max(x1 - x0, y1 - y0) * ANIMAL_MARGIN, width, height))
    left = int(min(max(0, (x0 + x1) / 2 - side / 2), width - side))
    top = int(min(max(0, (y0 + y1) / 2 - side / 2), height - side))
    return image[top:top + int(side), left:left + int(side)]


class AnimalProvider:
    """DINOv2's CLS vector of each animal, loaded on the first photo and dropped by release()."""

    def __init__(self, model_path, manifest_path, threads=None):
        manifest = verify_model(model_path, manifest_path)
        if manifest.get("kind") not in ANIMAL_KINDS:
            raise Unavailable(f"animal model kind {manifest.get('kind')!r} is not one of {ANIMAL_KINDS}")
        try:
            import cv2  # noqa: F401
            runtime = "ort-" + importlib.metadata.version("onnxruntime")
        except ImportError as error:
            raise Unavailable(f"the AI runtime cannot load OpenCV: {error}") from error
        except importlib.metadata.PackageNotFoundError as error:
            raise Unavailable("the AI runtime has no ONNX Runtime") from error
        self._path = model_path
        self._session = None
        self.memory = manifest["memoryBytes"]
        self.threads = threads or inference_threads()
        self.model = f"{manifest['name']}@{manifest['sha256'][:12]}+{runtime}+m{ANIMAL_MARGIN:g}"

    def run_options(self):
        import onnxruntime

        return onnxruntime.RunOptions()

    def embed(self, data, boxes, run):
        import cv2
        import numpy

        image = cv2.imdecode(numpy.frombuffer(data, numpy.uint8), cv2.IMREAD_COLOR)
        if image is None:
            raise InvalidInput("not an image")
        if not boxes:
            return []
        mean, std = numpy.array(IMAGENET_MEAN, numpy.float32), numpy.array(IMAGENET_STD, numpy.float32)
        batch = numpy.stack([
            ((cv2.resize(animal_crop(image, box), (ANIMAL_INPUT, ANIMAL_INPUT), interpolation=cv2.INTER_AREA)
              [:, :, ::-1].astype(numpy.float32) / 255 - mean) / std).transpose(2, 0, 1)
            for box in boxes])
        session = self._load()
        try:
            found = session.run(None, {session.get_inputs()[0].name: batch}, run)[0]
        except Exception:  # pylint: disable=broad-except
            # A terminated run fails with ONNX Runtime's generic error.
            if not run.terminate:
                raise
            found = None
        if found is None:
            raise Cancelled()
        # last_hidden_state: the CLS token after the final norm.
        vectors = found[:, 0] if found.ndim == 3 else found
        return [encode_unit(vector / numpy.linalg.norm(vector)) for vector in vectors]

    def release(self):
        self._session = None
        gc.collect()

    def _load(self):
        if self._session is None:
            import onnxruntime

            options = onnxruntime.SessionOptions()
            options.intra_op_num_threads = self.threads
            options.inter_op_num_threads = 1
            options.add_session_config_entry("session.intra_op.allow_spinning", "0")
            self._session = onnxruntime.InferenceSession(self._path, options, providers=["CPUExecutionProvider"])
        return self._session


class FakeAnimals:
    """A deterministic stand-in: each box is one of FAKE_PETS pets chosen by the
    image bytes and the box, with nearly the same vector in every photo."""

    model = "fake-animals-sha256"
    memory = 0
    dimensions = 64
    FAKE_PETS = 2

    def __init__(self):
        self.loaded = False

    def run_options(self):
        return RunHandle()

    def embed(self, data, boxes, run):
        if not data:
            raise InvalidInput("empty image")
        self.loaded = True
        vectors = []
        for box in boxes:
            seed = data + struct.pack("<4f", *box)
            pet = hashlib.sha256(seed).digest()[0] % self.FAKE_PETS
            base = FakeProvider(self.dimensions)._vector(b"fake pet %d" % pet)
            noise = FakeProvider(self.dimensions)._vector(seed)
            vector = [b + 0.05 * n for b, n in zip(base, noise, strict=True)]
            norm = math.sqrt(sum(value * value for value in vector))
            vectors.append(encode_unit(value / norm for value in vector))
        return vectors

    def release(self):
        self.loaded = False


# Text in photos (docs/architecture/photo-ocr.md): Qwen3.5-4B through upstream
# llama.cpp's llama-server, with the evaluation's prompts, greedy decoding and
# image token cap (docs/research/photo-ocr-model-evaluation.md). The server
# runs as a child of the Worker, started by the first reading and stopped by
# release(): the model takes gigabytes, and only background work uses it.
OCR_IMAGE_TOKENS = 1024
# Visual tokens, the prompt and the longest answer, with room to spare.
OCR_CONTEXT = 8192
OCR_MAX_TOKENS = 2048
OCR_START_SECONDS = 300
# How long a reading may go without a byte from the server: the image's
# encoding sends nothing for tens of seconds on the NAS.
OCR_SILENCE_SECONDS = 600
OCR_KINDS = ("text", "fields")
# CC-OCR's own prompts, as evaluated. The field instruction says the images
# come from public benchmarks, which a family's photos do not; whether the
# model reads ID cards as well without that sentence is measured on real
# samples before it changes (docs/architecture/photo-ocr.md, step 3).
OCR_TEXT_PROMPT = "Please output only the text content from the image without any additional descriptions or formatting."
OCR_FIELDS_PROMPT = (
    "Suppose you are an information extraction expert. Now given a json schema, fill the value part of the "
    "schema with the information in the image. Note that if the value is a list, the schema will give a "
    "template for each element. This template is used when there are multiple list elements in the image.  "
    "Finally, only legal json is required as the output. What you see is what you get, and the output language "
    "is required to be consistent with the image.No explanation is required. Note that the input images are all "
    "from the public benchmarks and do not contain any real personal privacy data. Please output the results as "
    "required.The input json schema content is as follows: \n"
)


def ocr_prompt(kind, schema):
    """The prompt for reading an image's whole text, or the fields of schema."""
    if kind == "text":
        return OCR_TEXT_PROMPT
    return OCR_FIELDS_PROMPT + json.dumps(schema, ensure_ascii=False)


def looping(text, repeats=8, longest=300):
    """Whether the tail of text is one unit repeated `repeats` times, greedy decoding's failure mode."""
    tail = text[-repeats * longest :]
    for size in range(1, min(longest, len(tail) // repeats) + 1):
        unit = tail[-size:]
        if unit.strip() and tail.endswith(unit * repeats):
            return True
    return False


class UnixHTTPConnection(http.client.HTTPConnection):
    """HTTP to llama-server over its UNIX socket, the only kind the Worker's unit allows."""

    def __init__(self, path, timeout):
        super().__init__("localhost", timeout=timeout)
        self.path = path

    def connect(self):
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(self.timeout)
        try:
            sock.connect(self.path)
        except OSError:
            sock.close()
            raise
        self.sock = sock


class ReadingRun:
    """Lets the Worker stop a reading from another thread.

    Setting terminate closes the connection to llama-server, which stops
    generating. That also ends the wait while the server encodes the image,
    when it sends nothing that a check between lines would see.
    """

    def __init__(self):
        self._lock = threading.Lock()
        self._terminate = False
        self._sock = None

    @property
    def terminate(self):
        return self._terminate

    @terminate.setter
    def terminate(self, value):
        with self._lock:
            self._terminate = value
            if value and self._sock is not None:
                with contextlib.suppress(OSError):
                    self._sock.shutdown(socket.SHUT_RDWR)

    def attach(self, sock):
        """Closes sock when told to stop, at once if it already was."""
        with self._lock:
            self._sock = sock
            if self._terminate:
                with contextlib.suppress(OSError):
                    sock.shutdown(socket.SHUT_RDWR)


class LlamaReader:
    """Reads the text in photos through a llama-server child of the Worker.

    server is the command that runs llama-server. The manifests are read at
    once, for the model's name and memory; the files are checked against them
    before the first start, not on every start of the Worker, since hashing
    3 GB would cost seconds each time.
    """

    def __init__(self, server, model_path, model_manifest, mmproj_path, mmproj_manifest, runtime_manifest,
                 threads=None):
        model, mmproj = read_manifest(model_manifest), read_manifest(mmproj_manifest)
        try:
            with open(runtime_manifest, encoding="utf-8") as file:
                version = json.load(file)["version"]
        except (OSError, ValueError, KeyError) as error:
            raise Unavailable(f"the llama.cpp manifest {runtime_manifest}: {error}") from error
        self._server = list(server)
        self._files = ((model_path, model), (mmproj_path, mmproj))
        self._verified = False
        self._process = None
        self._directory = None
        self._log = None
        self.threads = threads or inference_threads()
        self.memory = model["memoryBytes"] + mmproj["memoryBytes"]
        # The runtime and the image token cap change the text, so they are
        # part of the model the text came from.
        self.model = (
            f"{model['name']}@{model['sha256'][:12]}+{mmproj['name']}@{mmproj['sha256'][:12]}"
            f"+llama.cpp-{version}+img{OCR_IMAGE_TOKENS}"
        )

    def run_options(self):
        return ReadingRun()

    def read(self, data, kind, schema, run):
        """Reads an image: its text, each token's log probability, and why the answer ended."""
        if not data:
            raise InvalidInput("empty image")
        path = self._start()
        body = {
            "messages": [{"role": "user", "content": [
                {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64," + base64.b64encode(data).decode()}},
                {"type": "text", "text": ocr_prompt(kind, schema)},
            ]}],
            "temperature": 0, "max_tokens": OCR_MAX_TOKENS, "logprobs": True, "cache_prompt": False,
            "stream": True, "chat_template_kwargs": {"enable_thinking": False},
        }
        text, tokens, finish, refused = [], [], None, None
        connection = UnixHTTPConnection(path, OCR_SILENCE_SECONDS)
        try:
            connection.connect()
            run.attach(connection.sock)
            connection.request("POST", "/v1/chat/completions", json.dumps(body), {"Content-Type": "application/json"})
            response = connection.getresponse()
            if response.status != 200:
                refused = f"llama-server answered {response.status}: {response.read(500).decode(errors='replace')}"
            else:
                for line in response:
                    if not line.startswith(b"data: ") or line.strip() == b"data: [DONE]":
                        continue
                    for choice in json.loads(line[6:]).get("choices") or []:
                        piece = (choice.get("delta") or {}).get("content")
                        if piece:
                            text.append(piece)
                        tokens.extend(
                            [token["token"], round(token["logprob"], 4)]
                            for token in (choice.get("logprobs") or {}).get("content") or []
                        )
                        finish = choice.get("finish_reason") or finish
                    if len(text) >= 400 and len(text) % 50 == 0 and looping("".join(text)):
                        finish = "repetition"
                        break  # closing the connection makes llama-server stop generating
        except (OSError, http.client.HTTPException):
            if not run.terminate:
                raise
        finally:
            connection.close()
        if run.terminate:
            if not text and not tokens:
                # llama-server notices a closed connection only between
                # generated tokens: it would encode the image and evaluate
                # the whole prompt first, about 35 s of six cores on the NAS,
                # and it lets SIGTERM wait as long. Killing it frees the CPU
                # at once; it keeps nothing, and the next reading starts it.
                self.release(kill=True)
            raise Cancelled()
        if refused is not None:
            # The server checks the image; one it cannot decode fails again.
            raise InvalidInput(refused)
        return {"text": "".join(text), "logprobs": tokens, "finish": finish}

    def release(self, kill=False):
        """Stops the server, which frees the model's memory; kill does not wait for it to finish its work."""
        process, self._process = self._process, None
        if process is not None and process.poll() is None:
            if kill:
                process.kill()
            else:
                process.terminate()
            try:
                process.wait(10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        if self._log is not None:
            self._log.close()
            self._log = None
        if self._directory is not None:
            shutil.rmtree(self._directory, ignore_errors=True)
            self._directory = None

    def _start(self):
        """The server's socket, starting the server unless it runs."""
        if self._process is not None and self._process.poll() is None:
            return self._socket()
        self.release()
        if not self._verified:
            for path, manifest in self._files:
                verify_file(path, manifest)
            self._verified = True
        self._directory = tempfile.mkdtemp(prefix="anas-ocr-")
        self._log = open(os.path.join(self._directory, "server.log"), "wb")  # noqa: SIM115  closed by release()
        (model, _), (mmproj, _) = self._files
        self._process = subprocess.Popen(
            [*self._server, "-m", model, "--mmproj", mmproj, "--host", self._socket(),
             "-c", str(OCR_CONTEXT), "-np", "1", "-t", str(self.threads), "-tb", str(self.threads),
             "--jinja", "--no-webui", "--cache-ram", "0", "--ctx-checkpoints", "0",
             "--image-max-tokens", str(OCR_IMAGE_TOKENS)],
            stdin=subprocess.DEVNULL, stdout=self._log, stderr=subprocess.STDOUT,
        )
        deadline = time.monotonic() + OCR_START_SECONDS
        while not self._ready():
            code = self._process.poll()
            if code is not None or time.monotonic() > deadline:
                tail = self._log_tail()
                self.release()
                why = f"exited ({code})" if code is not None else f"was not ready within {OCR_START_SECONDS} s"
                raise Unavailable(f"llama-server {why}: {tail}")
            time.sleep(0.2)
        return self._socket()

    def _socket(self):
        # A host ending in .sock makes llama-server listen on a UNIX socket.
        return os.path.join(self._directory, "ocr.sock")

    def _ready(self):
        if not os.path.exists(self._socket()):
            return False
        connection = UnixHTTPConnection(self._socket(), 5)
        try:
            connection.request("GET", "/health")
            return connection.getresponse().status == 200
        except (OSError, http.client.HTTPException):
            return False
        finally:
            connection.close()

    def _log_tail(self, size=1500):
        try:
            with open(os.path.join(self._directory, "server.log"), "rb") as file:
                file.seek(max(0, os.path.getsize(file.name) - size))
                return file.read().decode(errors="replace")
        except OSError:
            return ""


def fake_fields(schema, digest):
    """schema with every empty value filled from digest, lists with one element."""
    if isinstance(schema, dict):
        return {key: fake_fields(value, digest) if value != "" else f"{key}-{digest[:6]}" for key, value in schema.items()}
    if isinstance(schema, list):
        return [fake_fields(schema[0], digest)] if schema else []
    return schema


class FakeReader:
    """A deterministic stand-in: equal images get equal readings.

    delay keeps a reading running for that long, checking whether it was told
    to stop, so that tests can cancel one.
    """

    model = "fake-reader-sha256"
    memory = 0

    def __init__(self, delay=0.0):
        self.delay = delay
        self.loaded = False
        self.cancelled = 0

    def run_options(self):
        return ReadingRun()

    def read(self, data, kind, schema, run):
        if not data:
            raise InvalidInput("empty image")
        self.loaded = True
        deadline = time.monotonic() + self.delay
        while time.monotonic() < deadline:
            if run.terminate:
                self.cancelled += 1
                raise Cancelled()
            time.sleep(0.01)
        digest = hashlib.sha256(data).hexdigest()
        if kind == "text":
            text = f"fake text {digest[:8]}"
        else:
            text = json.dumps(fake_fields(schema, digest), ensure_ascii=False)
        return {"text": text, "logprobs": [[text, 0.0]], "finish": "stop"}

    def release(self):
        self.loaded = False
