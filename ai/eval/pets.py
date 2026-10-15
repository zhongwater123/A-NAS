"""Finds the cats and dogs in the evaluation photos and embeds each one.

Development only: the pet evaluation behind docs/architecture/photo-people.md.
Each photo goes through the photo service's AI input (eval.faces.ai_input),
the object detector the Worker ships finds the cats and dogs in it
(anas_ai.providers.OnnxDetector, kept at the "检测到" scores of the
calibration: cat 0.6, dog 0.65), and every candidate embeds a square crop
around each animal:

- gemma: EmbeddingGemma's image vector of the crop through the thumbnail
  path, as the photo service would without a new model;
- dinov2: DINOv2-small (Apache-2.0), the CLS vector after the last norm;
- avito: AvitoTech's DINOv2-small fine-tuned for cat and dog identification
  (Apache-2.0 weights), exported to ONNX from its safetensors.

A set whose images are already one animal's face (DogFaceNet) skips the
detector: the whole image is the crop. The cache under
$ANAS_DATASETS/cache/pets keeps, per photo, the boxes (class, score, corners)
and each embedder's vectors. Runs resume where they stopped.

    python -m eval.pets --set mpdd --detector /models/dfine_m_obj365.onnx \\
        --dinov2 /pets/dinov2-small/model.onnx --avito /pets/avito-dinov2-small/model.onnx \\
        --gemma /models-eg/embeddinggemma-2-740m.litertlm
"""

import argparse
import io
import os
import time

import cv2
import numpy as np
from PIL import Image

from anas_ai import providers
from eval import datasets, pet_sets
from eval.embed import MANIFEST as GEMMA_MANIFEST, peak_rss_mib, render
from eval.faces import ai_input

DETECTOR_MANIFEST = os.path.join(datasets.REPO, "deploy", "models", "dfine-m-obj365.json")
# Objects365 classes and the scores search calls "检测到"
# (internal/photos/objects/v1.calibration.json).
CAT, DOG = 140, 93
KEEP = {CAT: 0.6, DOG: 0.65}
KINDS = {CAT: "cat", DOG: "dog"}
CROP_MARGIN = 1.1
IMAGENET_MEAN = np.array([0.485, 0.456, 0.406], np.float32)
IMAGENET_STD = np.array([0.229, 0.224, 0.225], np.float32)


def square_crop(image, box):
    """The square around a box of fractions, CROP_MARGIN times its longer side, inside the image."""
    height, width = image.shape[:2]
    x0, y0, x1, y1 = box[0] * width, box[1] * height, box[2] * width, box[3] * height
    side = min(max(x1 - x0, y1 - y0) * CROP_MARGIN, width, height)
    cx, cy = (x0 + x1) / 2, (y0 + y1) / 2
    left = int(min(max(0, cx - side / 2), width - side))
    top = int(min(max(0, cy - side / 2), height - side))
    return image[top:top + int(side), left:left + int(side)]


class Dino:
    """A DINOv2 ONNX export: pixel_values N x 3 x 224 x 224 in, the CLS token of last_hidden_state out."""

    def __init__(self, name, model):
        import onnxruntime

        self.name = name
        options = onnxruntime.SessionOptions()
        options.intra_op_num_threads = max(1, len(os.sched_getaffinity(0)) - 2)
        options.add_session_config_entry("session.intra_op.allow_spinning", "0")
        self._session = onnxruntime.InferenceSession(model, options, providers=["CPUExecutionProvider"])
        self._input = self._session.get_inputs()[0].name
        outputs = [output.name for output in self._session.get_outputs()]
        self._output = "pooler_output" if "pooler_output" in outputs else outputs[0]

    def embed(self, crops):
        batch = np.stack([
            ((cv2.resize(crop, (224, 224), interpolation=cv2.INTER_AREA)[:, :, ::-1].astype(np.float32) / 255
              - IMAGENET_MEAN) / IMAGENET_STD).transpose(2, 0, 1) for crop in crops])
        found = self._session.run([self._output], {self._input: batch})[0]
        vectors = found[:, 0] if found.ndim == 3 else found
        return vectors / np.linalg.norm(vectors, axis=1, keepdims=True)


class Gemma:
    """EmbeddingGemma's image vector of each crop, through the photo service's thumbnail."""

    name = "gemma"

    def __init__(self, model, cache_dir):
        self._provider = providers.MediaPipeProvider(model, GEMMA_MANIFEST, vision_tokens=70, cache_dir=cache_dir)

    def embed(self, crops):
        vectors = []
        for crop in crops:
            image = Image.fromarray(crop[:, :, ::-1])
            vectors.append(np.asarray(self._provider.embed_image(render(image, 512)), np.float32))
        vectors = np.stack(vectors)
        return vectors / np.linalg.norm(vectors, axis=1, keepdims=True)


def detect(detector, image):
    """[class, score, x0, y0, x1, y1] of the cats and dogs at their kept scores, best first."""
    ok, encoded = cv2.imencode(".jpg", image, [cv2.IMWRITE_JPEG_QUALITY, 92])
    boxes = detector.detect(encoded.tobytes(), detector.run_options())
    return [box for box in boxes if box[0] in KEEP and box[1] >= KEEP[box[0]]]


class Cache:
    """Per photo: the animals found (class, score, corners) and each embedder's vectors."""

    def __init__(self, path):
        self.path = path
        self.photos = {}
        if os.path.exists(path):
            with np.load(path, allow_pickle=False) as data:
                keys, counts, boxes = data["keys"].tolist(), data["counts"].tolist(), data["boxes"]
                # Each array once: indexing the archive reads it again every time.
                vectors = {name[2:]: data[name] for name in data.files if name.startswith("v_")}
                missing = {name: data["m_" + name] for name in vectors}
                start = 0
                for index, (key, count) in enumerate(zip(keys, counts)):
                    self.photos[key] = {
                        "boxes": boxes[start:start + count],
                        "vectors": {name: found[start:start + count] for name, found in vectors.items()
                                    if not missing[name][index]},
                    }
                    start += count

    def save(self):
        keys = sorted(self.photos)
        dimensions = {name: found.shape[1] for photo in self.photos.values() for name, found in photo["vectors"].items()}
        arrays = {
            "keys": np.array(keys),
            "counts": np.array([len(self.photos[key]["boxes"]) for key in keys], np.int64),
            "boxes": np.concatenate([np.asarray(self.photos[key]["boxes"], np.float32).reshape(-1, 6) for key in keys]
                                    or [np.zeros((0, 6), np.float32)]),
        }
        for name, size in dimensions.items():
            arrays["v_" + name] = np.concatenate(
                [self.photos[key]["vectors"].get(name, np.zeros((len(self.photos[key]["boxes"]), size), np.float32))
                 for key in keys])
            arrays["m_" + name] = np.array([name not in self.photos[key]["vectors"] for key in keys])
        partial = self.path + ".partial.npz"
        np.savez(partial, **arrays)
        os.replace(partial, self.path)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--set", required=True, choices=sorted(pet_sets.SETS))
    parser.add_argument("--detector", help="the official D-FINE Objects365 export")
    parser.add_argument("--dinov2")
    parser.add_argument("--avito")
    parser.add_argument("--gemma")
    parser.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    parser.add_argument("--limit", type=int, default=0)
    args = parser.parse_args()

    embedders = []
    if args.dinov2:
        embedders.append(Dino("dinov2", args.dinov2))
    if args.avito:
        embedders.append(Dino("avito", args.avito))
    if args.gemma:
        os.makedirs(args.weight_cache, exist_ok=True)
        embedders.append(Gemma(args.gemma, args.weight_cache))
    photos = pet_sets.SETS[args.set]()
    whole = pet_sets.WHOLE.get(args.set)
    detector = None
    if whole is None:
        if not args.detector:
            raise SystemExit(f"--detector is needed for {args.set}")
        detector = providers.OnnxDetector(args.detector, DETECTOR_MANIFEST)
    if args.limit:
        photos = photos[: args.limit]
    directory = os.path.join(datasets.ROOT, "cache", "pets")
    os.makedirs(directory, exist_ok=True)
    cache = Cache(os.path.join(directory, f"{args.set}.npz"))
    todo = [photo for photo in photos if photo.key not in cache.photos
            or any(e.name not in cache.photos[photo.key]["vectors"] for e in embedders)]
    print(f"{args.set}: {len(photos)} photos, {len(todo)} to process", flush=True)
    started, done, timing = time.monotonic(), 0, {"detect": 0.0, **{e.name: 0.0 for e in embedders}}
    for photo in todo:
        try:
            image = ai_input(photo.path)
        except (OSError, ValueError) as error:
            print(f"skipped {photo.key}: {error}", flush=True)
            continue
        entry = cache.photos.get(photo.key)
        if entry is None:
            t = time.monotonic()
            boxes = [[whole, 1.0, 0.0, 0.0, 1.0, 1.0]] if whole is not None else detect(detector, image)
            timing["detect"] += time.monotonic() - t
            entry = cache.photos[photo.key] = {"boxes": np.asarray(boxes, np.float32).reshape(-1, 6), "vectors": {}}
        crops = [square_crop(image, box[2:]) for box in entry["boxes"]]
        for embedder in embedders:
            if embedder.name in entry["vectors"]:
                continue
            t = time.monotonic()
            entry["vectors"][embedder.name] = (embedder.embed(crops).astype(np.float32) if crops
                                               else np.zeros((0, 768 if embedder.name == "gemma" else 384), np.float32))
            timing[embedder.name] += time.monotonic() - t
        done += 1
        if done % 200 == 0 or done == len(todo):
            cache.save()
            per = {name: f"{1000 * value / done:.0f} ms" for name, value in timing.items()}
            print(f"{done}/{len(todo)} in {time.monotonic() - started:.0f} s, per photo {per}, peak RSS {peak_rss_mib():.0f} MiB",
                  flush=True)
    cache.save()


if __name__ == "__main__":
    main()
