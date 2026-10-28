"""Finds and embeds the faces in the evaluation photos and caches them.

Development only: the face evaluation behind docs/architecture/photo-people.md.
Each photo goes through the input the photo service renders for AI
(internal/photos/aiinput.go: upright, fit within 1600 px, JPEG quality 92).
YuNet finds the faces and their five landmarks, optionally on a smaller copy
(--detect-edge) to measure what a cheaper detection loses; each face is then
aligned to 112 x 112 on the full input and embedded by every candidate:

- sface: OpenCV Zoo's SFace through cv2.FaceRecognizerSF (128 dimensions);
- auraface: fal's AuraFace v1 (ResNet100, 512 dimensions) through ONNX Runtime.

The cache under $ANAS_DATASETS/cache/faces/<detector config> keeps, per photo,
every face found above --min-score with its box, landmarks and vectors, so
that the analysis can choose the score and size thresholds later. Runs resume
where they stopped.

    python -m eval.faces --set lfw --yunet /models/face_detection_yunet_2026may.onnx \\
        --sface /models/face_recognition_sface_2021dec.onnx --auraface /models/auraface/glintr100.onnx
"""

import argparse
import io
import json
import os
import time

import cv2
import numpy as np
from PIL import Image, ImageOps

from eval import datasets, face_sets
from eval.embed import peak_rss_mib

# The photo service's AI input (internal/photos/aiinput.go).
AI_INPUT_EDGE = 1600
AI_INPUT_QUALITY = 92
# Faces kept for analysis; what counts as a face is chosen later.
MIN_SCORE = 0.5
NMS = 0.3
ALIGNED = 112


def ai_input(path, edge=AI_INPUT_EDGE):
    """The photo service's AI input for the image at path, decoded as BGR."""
    with Image.open(path) as opened:
        image = ImageOps.exif_transpose(opened).convert("RGB")
        width, height = image.size
        if max(width, height) > edge:
            scale = edge / max(width, height)
            image = image.resize((max(1, round(width * scale)), max(1, round(height * scale))), Image.Resampling.LANCZOS)
        out = io.BytesIO()
        image.save(out, format="JPEG", quality=AI_INPUT_QUALITY)
    return cv2.imdecode(np.frombuffer(out.getvalue(), np.uint8), cv2.IMREAD_COLOR)


class Detector:
    """YuNet at a given input edge; faces come back in the full image's pixels."""

    def __init__(self, model, detect_edge=0, min_score=MIN_SCORE):
        self.detect_edge = detect_edge
        self._net = cv2.FaceDetectorYN.create(model, "", (320, 320), min_score, NMS, 5000)
        self.name = f"{os.path.basename(model)}-d{detect_edge or 'full'}-s{min_score:g}"

    def detect(self, image):
        """Rows of x, y, w, h, five landmark points (x, y) and the score."""
        height, width = image.shape[:2]
        scale = 1.0
        if self.detect_edge and max(width, height) > self.detect_edge:
            scale = self.detect_edge / max(width, height)
            image = cv2.resize(image, (max(1, round(width * scale)), max(1, round(height * scale))),
                               interpolation=cv2.INTER_AREA)
        self._net.setInputSize((image.shape[1], image.shape[0]))
        _, faces = self._net.detect(image)
        if faces is None:
            return np.zeros((0, 15), np.float32)
        faces = faces.astype(np.float32)
        faces[:, :14] /= scale
        return faces


class SFace:
    name = "sface"

    def __init__(self, model):
        self._net = cv2.FaceRecognizerSF.create(model, "")

    def align(self, image, face):
        return self._net.alignCrop(image, face)

    def embed(self, crops):
        vectors = np.stack([self._net.feature(crop).reshape(-1) for crop in crops])
        return vectors / np.linalg.norm(vectors, axis=1, keepdims=True)


class AuraFace:
    """ArcFace-style: RGB, (x - 127.5) / 127.5, N x 3 x 112 x 112."""

    name = "auraface"

    def __init__(self, model, threads=None):
        import onnxruntime

        options = onnxruntime.SessionOptions()
        options.intra_op_num_threads = threads or max(1, len(os.sched_getaffinity(0)) - 2)
        options.add_session_config_entry("session.intra_op.allow_spinning", "0")
        self._session = onnxruntime.InferenceSession(model, options, providers=["CPUExecutionProvider"])
        self._input = self._session.get_inputs()[0].name

    def embed(self, crops):
        blob = cv2.dnn.blobFromImages(crops, 1 / 127.5, (ALIGNED, ALIGNED), (127.5, 127.5, 127.5), swapRB=True)
        vectors = self._session.run(None, {self._input: blob})[0]
        return vectors / np.linalg.norm(vectors, axis=1, keepdims=True)


class Cache:
    """Per photo: the faces found (rows as Detector.detect) and each embedder's vectors."""

    def __init__(self, path):
        self.path = path
        self.photos = {}
        if os.path.exists(path):
            with np.load(path, allow_pickle=False) as data:
                keys = data["keys"].tolist()
                counts = data["counts"].tolist()
                faces = data["faces"]
                vectors = {name[len("v_"):]: data[name] for name in data.files if name.startswith("v_")}
                missing = {name: data["m_" + name] for name in vectors}
                start = 0
                for index, (key, count) in enumerate(zip(keys, counts, strict=True)):
                    self.photos[key] = {
                        "faces": faces[start:start + count],
                        "vectors": {name: found[start:start + count] for name, found in vectors.items()
                                    if not missing[name][index]},
                    }
                    start += count

    def save(self):
        keys = sorted(self.photos)
        dimensions = {}
        for photo in self.photos.values():
            for name, found in photo["vectors"].items():
                dimensions[name] = found.shape[1]
        arrays = {
            "keys": np.array(keys),
            "counts": np.array([len(self.photos[key]["faces"]) for key in keys], np.int64),
            "faces": np.concatenate([self.photos[key]["faces"] for key in keys] or [np.zeros((0, 15), np.float32)]),
        }
        # A photo an embedder has not reached yet is padded and marked missing.
        for name, size in dimensions.items():
            arrays["v_" + name] = np.concatenate(
                [self.photos[key]["vectors"].get(name, np.zeros((len(self.photos[key]["faces"]), size), np.float32))
                 for key in keys])
            arrays["m_" + name] = np.array([name not in self.photos[key]["vectors"] for key in keys])
        partial = self.path + ".partial.npz"
        np.savez(partial, **arrays)
        os.replace(partial, self.path)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", required=True, choices=sorted(face_sets.SETS))
    parser.add_argument("--yunet", required=True)
    parser.add_argument("--sface")
    parser.add_argument("--auraface")
    parser.add_argument("--detect-edge", type=int, default=0, help="run YuNet on a copy at most this large (0: the full input)")
    parser.add_argument("--min-score", type=float, default=MIN_SCORE)
    parser.add_argument("--limit", type=int, default=0)
    parser.add_argument("--detect-only", action="store_true", help="find faces without embedding them (WIDER FACE)")
    args = parser.parse_args()

    detector = Detector(args.yunet, args.detect_edge, args.min_score)
    embedders = []
    if args.detect_only:
        args.sface = args.auraface = None
    if args.sface:
        embedders.append(SFace(args.sface))
    if args.auraface:
        embedders.append(AuraFace(args.auraface))
    aligner = embedders[0] if embedders and isinstance(embedders[0], SFace) else None
    if embedders and aligner is None:
        raise SystemExit("--sface is needed: its alignCrop aligns every embedder's input")

    directory = os.path.join(datasets.ROOT, "cache", "faces", detector.name)
    os.makedirs(directory, exist_ok=True)
    cache = Cache(os.path.join(directory, f"{args.set}.npz"))
    photos = face_sets.SETS[args.set]()
    if args.limit:
        photos = photos[: args.limit]
    todo = [photo for photo in photos if photo.key not in cache.photos
            or any(e.name not in cache.photos[photo.key]["vectors"] for e in embedders)]
    print(f"{args.set}: {len(photos)} photos, {len(todo)} to process", flush=True)
    timing = {"render": 0.0, "detect": 0.0, **{e.name: 0.0 for e in embedders}}
    started, failed, processed = time.monotonic(), 0, 0
    faces_seen = 0

    def progress(done):
        elapsed = time.monotonic() - started
        per = {name: f"{1000 * value / max(done, 1):.0f} ms" for name, value in timing.items()}
        print(f"{done}/{len(todo)} in {elapsed:.0f} s, {faces_seen} faces, per photo {json.dumps(per)}, "
              f"peak RSS {peak_rss_mib():.0f} MiB", flush=True)

    for photo in todo:
        try:
            t = time.monotonic()
            image = ai_input(photo.path)
            timing["render"] += time.monotonic() - t
        except (OSError, ValueError) as error:
            failed += 1
            print(f"skipped {photo.key}: {error}", flush=True)
            continue
        entry = cache.photos.get(photo.key)
        if entry is None:
            t = time.monotonic()
            faces = detector.detect(image)
            timing["detect"] += time.monotonic() - t
            entry = cache.photos[photo.key] = {"faces": faces, "vectors": {}}
        faces = entry["faces"]
        faces_seen += len(faces)
        crops = [aligner.align(image, face) for face in faces] if embedders else []
        for embedder in embedders:
            if embedder.name in entry["vectors"]:
                continue
            t = time.monotonic()
            dimensions = 128 if embedder.name == "sface" else 512
            entry["vectors"][embedder.name] = (embedder.embed(crops).astype(np.float32) if crops
                                               else np.zeros((0, dimensions), np.float32))
            timing[embedder.name] += time.monotonic() - t
        processed += 1
        if processed % 200 == 0:
            cache.save()
            progress(processed)
    cache.save()
    progress(processed)
    if failed:
        print(f"{failed} photos could not be read", flush=True)


if __name__ == "__main__":
    main()
