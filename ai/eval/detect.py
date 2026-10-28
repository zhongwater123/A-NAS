"""Runs an Objects365 detector over the evaluation images and caches its boxes.

Development only: this is the evaluation of the object detector beside the
image vectors (docs/research/photo-ai-object-detection.md). The detector is
D-FINE trained on Objects365 (365 classes), run with ONNX Runtime on the CPU.
Each image gets its boxes above a low score, so that the analysis can choose
thresholds later; boxes are normalised corners (x0, y0, x1, y1).

--model takes either the Transformers.js conversion's directory (config.json
and onnx/), measured first, or the official export's .onnx file, which runs
through the Worker's own provider (anas_ai.providers.OnnxDetector) with the
manifest from deploy/models, so that what is measured is what ships:

    python -m eval.detect --model /models/dfine_m_obj365 --sets coco,coco-cn
    python -m eval.detect --model /models/official/dfine_m_obj365.onnx --sets coco,coco-cn,openimages

--bench N times the detector against the photo vectors' EmbeddingGemma on the
same N images, interleaved so that both meet the same load, and reports wall
and process CPU time per image:

    python -m eval.detect --model /models/dfine_m_obj365 --bench 40 \\
        --embedder /models-eg/embeddinggemma-2-740m.litertlm
"""

import argparse
import hashlib
import json
import os
import statistics
import time

import numpy as np
from PIL import Image, ImageOps

from eval import datasets
from eval.embed import MANIFEST, image_jobs, peak_rss_mib, thumbnail

INPUT = 640  # the model's square input; D-FINE resizes without keeping the aspect ratio
MIN_SCORE = 0.05  # boxes kept for analysis; shown or searched boxes need far more
MAX_BOXES = 100


class Detector:
    def __init__(self, directory, threads, variant="model.onnx"):
        import onnxruntime

        threads = threads or 4
        self.path = os.path.join(directory, "onnx", variant)
        with open(os.path.join(directory, "config.json"), encoding="utf-8") as file:
            self.labels = {int(key): name for key, name in json.load(file)["id2label"].items()}
        revision = ""
        hub = os.path.join(directory, "hub.json")
        if os.path.exists(hub):
            with open(hub, encoding="utf-8") as file:
                revision = json.load(file)["sha"]
        with open(self.path, "rb") as file:
            self.sha256 = hashlib.sha256(file.read()).hexdigest()
        options = onnxruntime.SessionOptions()
        options.intra_op_num_threads = threads
        options.inter_op_num_threads = 1
        self.session = onnxruntime.InferenceSession(self.path, options, providers=["CPUExecutionProvider"])
        self.input = self.session.get_inputs()[0].name
        self.model = (f"{os.path.basename(directory.rstrip('/'))}@{self.sha256[:12]}"
                      f"+ort-{onnxruntime.__version__}+{INPUT}")
        self.revision = revision

    @staticmethod
    def pixels(path):
        with Image.open(path) as opened:
            image = ImageOps.exif_transpose(opened).convert("RGB")
            resized = image.resize((INPUT, INPUT), Image.Resampling.BILINEAR)
        return (np.asarray(resized, dtype=np.float32) / 255.0).transpose(2, 0, 1)[None]

    def detect(self, pixels):
        """Returns classes, scores and corner boxes, best first."""
        logits, boxes = self.session.run(None, {self.input: pixels})
        scores = 1 / (1 + np.exp(-logits[0]))
        flat = scores.reshape(-1)
        top = np.argpartition(-flat, MAX_BOXES)[:MAX_BOXES]
        top = top[np.argsort(-flat[top])]
        top = top[flat[top] >= MIN_SCORE]
        query, classes = np.divmod(top, scores.shape[1])
        cx, cy, w, h = boxes[0][query].T
        corners = np.stack([cx - w / 2, cy - h / 2, cx + w / 2, cy + h / 2], axis=1).clip(0, 1)
        return classes.astype(np.int16), flat[top].astype(np.float32), corners.astype(np.float32)


DETECTOR_MANIFEST = os.path.join(datasets.REPO, "deploy", "models", "dfine-m-obj365.json")
CLASSES = os.path.join(datasets.REPO, "internal", "photos", "objects", "v1.json")


class ProductDetector:
    """The official export through the Worker's own provider."""

    def __init__(self, path, threads):
        from anas_ai import providers

        self.provider = providers.OnnxDetector(path, DETECTOR_MANIFEST, threads)
        self.model = self.provider.model
        with open(DETECTOR_MANIFEST, encoding="utf-8") as file:
            manifest = json.load(file)
        self.sha256, self.revision = manifest["sha256"], manifest["revision"]
        with open(CLASSES, encoding="utf-8") as file:
            self.labels = {entry["id"]: entry["en"] for entry in json.load(file)["classes"]}

    @staticmethod
    def pixels(path):
        # The Worker decodes the JPEG the photo service sends; here the
        # dataset's own file.
        with open(path, "rb") as file:
            return file.read()

    def detect(self, data):
        boxes = self.provider.detect(data, self.provider.run_options())
        classes = np.array([box[0] for box in boxes], dtype=np.int16)
        scores = np.array([box[1] for box in boxes], dtype=np.float32)
        corners = np.array([box[2:] for box in boxes], dtype=np.float32).reshape(-1, 4)
        return classes, scores, corners


def open_detector(model, threads, variant):
    if model.endswith(".onnx"):
        return ProductDetector(model, threads)
    return Detector(model, threads, variant)


class Detections:
    """Boxes by image key in one .npz file, saved as the run goes."""

    def __init__(self, path):
        self.path = path
        self.found = {}
        if os.path.exists(path):
            self.found = load(path)

    def save(self):
        keys = sorted(self.found)
        counts = [len(self.found[key][0]) for key in keys]
        partial = self.path + ".partial.npz"
        np.savez(partial, keys=np.array(keys), offsets=np.concatenate([[0], np.cumsum(counts)]).astype(np.int64),
                 classes=np.concatenate([self.found[key][0] for key in keys]),
                 scores=np.concatenate([self.found[key][1] for key in keys]),
                 boxes=np.concatenate([self.found[key][2] for key in keys]).reshape(-1, 4))
        os.replace(partial, self.path)


def load(path):
    """{image key: (classes, scores, boxes)} from a detections file."""
    with np.load(path) as data:
        keys, offsets = data["keys"].tolist(), data["offsets"]
        classes, scores, boxes = data["classes"], data["scores"], data["boxes"]
    return {key: (classes[a:b], scores[a:b], boxes[a:b]) for key, a, b in zip(keys, offsets[:-1], offsets[1:], strict=True)}


def bench(detector, embedder_path, weight_cache, count, threads):
    """Times the detector and the vector model on the same COCO images."""
    jobs = image_jobs({"coco"})[:count]
    embedder = None
    if embedder_path:
        from anas_ai import providers

        os.makedirs(weight_cache, exist_ok=True)
        embedder = providers.MediaPipeProvider(embedder_path, MANIFEST, vision_tokens=70, cache_dir=weight_cache)
    # One warm-up each, outside the timings.
    detector.detect(detector.pixels(jobs[0][1]))
    if embedder:
        embedder.embed_image(thumbnail(jobs[0][1], 512))
    timings = {"detect": [], "embed": []}
    for _key, path in jobs:
        for name in ("detect", "embed"):
            if name == "embed" and not embedder:
                continue
            wall, cpu = time.perf_counter(), time.process_time()
            if name == "detect":
                detector.detect(detector.pixels(path))
            else:
                embedder.embed_image(thumbnail(path, 512))
            timings[name].append((time.perf_counter() - wall, time.process_time() - cpu))
    report = {"model": detector.model, "threads": threads, "images": len(jobs), "peakRssMiB": round(peak_rss_mib())}
    for name, found in timings.items():
        if found:
            report[name] = {"wallMedianS": round(statistics.median(w for w, _ in found), 3),
                            "cpuMedianS": round(statistics.median(c for _, c in found), 3)}
    if embedder:
        report["detectCpuPerEmbedCpu"] = round(report["detect"]["cpuMedianS"] / report["embed"]["cpuMedianS"], 2)
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model", required=True, help="the conversion's directory, or the official .onnx file")
    parser.add_argument("--variant", default="model.onnx")
    parser.add_argument("--threads", type=int, help="ONNX Runtime threads (default: 4, or the Worker's own choice)")
    parser.add_argument("--sets", default="coco,coco-cn")
    parser.add_argument("--limit", type=int, default=0, help="detect at most this many images per set (0: all)")
    parser.add_argument("--bench", type=int, default=0, help="time N images instead of caching boxes")
    parser.add_argument("--embedder", help="EmbeddingGemma .litertlm to time beside the detector")
    parser.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"),
                        help="writable directory for the embedder's repacked weights")
    args = parser.parse_args()

    started = time.monotonic()
    detector = open_detector(args.model, args.threads, args.variant)
    print(f"model {detector.model} (hub revision {detector.revision[:12]}) loaded in "
          f"{time.monotonic() - started:.1f} s, peak RSS {peak_rss_mib():.0f} MiB", flush=True)
    if args.bench:
        print(json.dumps(bench(detector, args.embedder, args.weight_cache, args.bench, args.threads),
                         ensure_ascii=False), flush=True)
        return

    directory = os.path.join(datasets.ROOT, "cache", detector.model)
    os.makedirs(directory, exist_ok=True)
    cache = Detections(os.path.join(directory, "detections.npz"))
    with open(os.path.join(directory, "labels.json"), "w", encoding="utf-8") as file:
        json.dump({"model": detector.model, "revision": detector.revision, "sha256": detector.sha256,
                   "labels": detector.labels}, file, ensure_ascii=False, indent=1)
    for name in args.sets.split(","):
        jobs = image_jobs({name})
        if name == "coco-cn":
            # Search is measured on the test split only.
            test = {image for image, _path in datasets.coco_cn("test")[0]}
            jobs = [job for job in jobs if job[0].split(":", 1)[1] in test]
        if args.limit:
            jobs = jobs[: args.limit]
        todo = [(key, path) for key, path in jobs if key not in cache.found]
        print(f"{name}: {len(jobs)} images, {len(todo)} to detect", flush=True)
        started, done, failed = time.monotonic(), 0, []
        for key, path in todo:
            try:
                cache.found[key] = detector.detect(detector.pixels(path))
            except OSError as error:
                failed.append((key, str(error)))
                continue
            done += 1
            if done % 250 == 0:
                cache.save()
                elapsed = time.monotonic() - started
                print(f"  {done}/{len(todo)}  {elapsed / done:.3f} s/image  peak RSS {peak_rss_mib():.0f} MiB", flush=True)
        if done:
            cache.save()
            print(f"  {done} images in {time.monotonic() - started:.0f} s "
                  f"({(time.monotonic() - started) / done:.3f} s/image)", flush=True)
        for key, error in failed:
            print(f"  failed {key}: {error}", flush=True)
    print(f"peak RSS {peak_rss_mib():.0f} MiB", flush=True)


if __name__ == "__main__":
    main()
