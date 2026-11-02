"""Runs a PP-OCRv6 text detector over the OCR routing evaluation's photos.

Development only (docs/architecture/photo-ocr.md#哪些照片读文字). Each photo
is first rendered as the photo service renders the detectors' input (upright,
at most 1600 px, JPEG quality 92: internal/photos/aiinput.go), then resized so
that its long side is --side pixels, both sides multiples of 32, and
normalised as the model's inference.yml says (BGR, ImageNet mean and standard
deviation). Of the probability map it keeps what routing can decide on:

- text: the share of the map above the model's thresh (0.2);
- boxes: connected regions of that mask whose mean probability reaches the
  model's box_thresh (0.45) and that cover at least MIN_BOX map pixels;
- box_share: the share those boxes cover;

and the seconds the model took. Results are cached per model and side under
$ANAS_DATASETS/cache, and a run resumes where it stopped.

    python -m eval.text_detect --model /pp-ocrv6/tiny/inference.onnx --side 640 --threads 4
    python -m eval.text_detect --model ... --side 640 --threads 6 --bench 50   # time only, on the NAS
"""

import argparse
import hashlib
import io
import json
import os
import time

import cv2
import numpy as np
import onnxruntime
from PIL import Image, ImageOps

from eval import datasets, text_sets

THRESH = 0.2
BOX_THRESH = 0.45
MIN_BOX = 9
AI_INPUT_EDGE, AI_INPUT_QUALITY = 1600, 92
MEAN = np.array([0.485, 0.456, 0.406], dtype=np.float32)
STD = np.array([0.229, 0.224, 0.225], dtype=np.float32)


def ai_input(path):
    """The JPEG the photo service sends the Worker's detectors."""
    with Image.open(path) as opened:
        image = ImageOps.exif_transpose(opened)
        if image.mode != "RGB":
            background = Image.new("RGB", image.size, "white")
            background.paste(image, mask=image.getchannel("A") if "A" in image.getbands() else None)
            image = background
        scale = AI_INPUT_EDGE / max(image.size)
        if scale < 1:
            image = image.resize((round(image.width * scale), round(image.height * scale)), Image.LANCZOS)
        buffer = io.BytesIO()
        image.save(buffer, "JPEG", quality=AI_INPUT_QUALITY)
        return buffer.getvalue()


def tensor(data, side):
    """The detector's input: the long side at side pixels, both sides multiples of 32."""
    image = cv2.imdecode(np.frombuffer(data, np.uint8), cv2.IMREAD_COLOR)  # BGR
    height, width = image.shape[:2]
    scale = side / max(height, width)
    size = (max(32, round(width * scale / 32) * 32), max(32, round(height * scale / 32) * 32))
    resized = cv2.resize(image, size, interpolation=cv2.INTER_AREA if scale < 1 else cv2.INTER_LINEAR)
    normalised = (resized.astype(np.float32) / 255.0 - MEAN) / STD
    return normalised.transpose(2, 0, 1)[np.newaxis]


def features(probability):
    """What routing decides on, from the detector's probability map."""
    mask = (probability > THRESH).astype(np.uint8)
    count, labels, stats, _ = cv2.connectedComponentsWithStats(mask, connectivity=8)
    sums = np.bincount(labels.ravel(), weights=probability.ravel(), minlength=count)
    areas = stats[:, cv2.CC_STAT_AREA]
    kept = [index for index in range(1, count) if areas[index] >= MIN_BOX and sums[index] / areas[index] >= BOX_THRESH]
    return {
        "text": float(mask.mean()),
        "boxes": len(kept),
        "box_share": float(sum(int(areas[index]) for index in kept) / probability.size),
    }


class Detector:
    def __init__(self, path, threads):
        options = onnxruntime.SessionOptions()
        options.intra_op_num_threads = threads
        options.inter_op_num_threads = 1
        options.add_session_config_entry("session.intra_op.allow_spinning", "0")
        self.session = onnxruntime.InferenceSession(path, options, providers=["CPUExecutionProvider"])
        self.input = self.session.get_inputs()[0].name
        with open(path, "rb") as file:
            self.sha256 = hashlib.sha256(file.read()).hexdigest()

    def detect(self, data, side):
        batch = tensor(data, side)
        started = time.monotonic()
        probability = self.session.run(None, {self.input: batch})[0][0, 0]
        found = features(probability)
        found["seconds"] = round(time.monotonic() - started, 4)
        return found


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model", required=True, help="a PP-OCRv6 detector, inference.onnx")
    parser.add_argument("--side", type=int, default=640, help="the long side the detector sees, in pixels")
    parser.add_argument("--threads", type=int, default=4)
    parser.add_argument("--sets", default="cc-ocr,coco,openimages,gallagher")
    parser.add_argument("--bench", type=int, default=0, help="only time this many photos of each kind")
    args = parser.parse_args()
    detector = Detector(args.model, args.threads)
    name = f"{os.path.basename(os.path.dirname(args.model))}@{detector.sha256[:12]}+ort-{onnxruntime.__version__}+{args.side}"
    samples = text_sets.samples(tuple(args.sets.split(",")))
    if args.bench:
        by_kind = {}
        for sample in samples:
            by_kind.setdefault(sample.kind, []).append(sample)
        samples = [sample for kind in sorted(by_kind) for sample in by_kind[kind][: args.bench]]
        times = []
        for sample in samples:
            data = ai_input(sample.path)
            started = time.monotonic()
            detector.detect(data, args.side)
            times.append(time.monotonic() - started)
        times.sort()
        print(json.dumps({"model": name, "threads": args.threads, "photos": len(times),
                          "median_s": round(times[len(times) // 2], 4), "p90_s": round(times[int(len(times) * 0.9)], 4)}))
        return
    path = os.path.join(datasets.ROOT, "cache", "text-detect", name + ".json")
    os.makedirs(os.path.dirname(path), exist_ok=True)
    results = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            results = json.load(file)
    todo = [sample for sample in samples if sample.key not in results]
    print(f"{name}: {len(samples)} photos, {len(todo)} to detect", flush=True)
    started = time.monotonic()

    def save():
        with open(path + ".partial", "w", encoding="utf-8") as file:
            json.dump(results, file)
        os.replace(path + ".partial", path)

    for done, sample in enumerate(todo, 1):
        try:
            results[sample.key] = detector.detect(ai_input(sample.path), args.side)
        except OSError as error:
            print(f"  failed {sample.key}: {error}", flush=True)
        if done % 500 == 0:
            save()
            print(f"  {done}/{len(todo)}  {(time.monotonic() - started) / done:.3f} s/photo", flush=True)
    save()
    print(f"done in {time.monotonic() - started:.0f} s", flush=True)


if __name__ == "__main__":
    main()
