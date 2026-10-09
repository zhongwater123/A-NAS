"""Embeds the evaluation images and texts with the real model.

Images go through the same thumbnail the photo service renders
(internal/photos/thumbnail.go: fit within 512 px with Lanczos, flatten on
white, JPEG quality 82) unless --edge says otherwise. Vectors are cached per
model and edge under $ANAS_DATASETS/cache and the run resumes where it
stopped.

    python -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --tokens 70

MediaPipe uses four threads; on a larger machine run several shards at once
(--shard 0/3, 1/3, 2/3), each writing its own file.
"""

import argparse
import hashlib
import io
import os
import resource
import time

import numpy as np
from PIL import Image, ImageOps

from anas_ai import providers
from eval import datasets

MANIFEST = os.path.join(datasets.REPO, "deploy", "models", "embeddinggemma-2-740m.json")

# Text templates compared by the calibration; images get no prompt.
LABEL_TEMPLATES = {
    "plain": "{}",
    "query": "task: search result | query: {}",
    "photo": "task: search result | query: 一张{}的照片",
}
CAPTION_TEMPLATES = {"plain": "{}", "query": "task: search result | query: {}"}


def thumbnail(path, edge):
    """Renders the photo service's thumbnail of the image at path."""
    with Image.open(path) as opened:
        image = ImageOps.exif_transpose(opened)
        width, height = image.size
        if width > edge or height > edge:
            # imaging.Fit: keep the aspect ratio, truncate the short side.
            if width / height > 1:
                size = (edge, max(1, int(edge / (width / height))))
            else:
                size = (max(1, int(edge * (width / height))), edge)
            image = image.resize(size, Image.Resampling.LANCZOS)
        if image.mode in ("RGBA", "LA", "P"):
            image = image.convert("RGBA")
            flat = Image.new("RGB", image.size, "white")
            flat.paste(image, mask=image.getchannel("A"))
            image = flat
        out = io.BytesIO()
        image.convert("RGB").save(out, format="JPEG", quality=82)
        return out.getvalue()


class Cache:
    """Vectors by key in one .npz file, saved as the run goes."""

    def __init__(self, path):
        self.path = path
        self.vectors = {}
        if os.path.exists(path):
            with np.load(path) as data:
                self.vectors = dict(zip(data["keys"].tolist(), data["vectors"]))

    def save(self):
        keys = sorted(self.vectors)
        partial = self.path + ".partial.npz"
        np.savez(partial, keys=np.array(keys), vectors=np.stack([self.vectors[key] for key in keys]))
        os.replace(partial, self.path)


def load_vectors(directory, prefix):
    """Merges the vectors of every <prefix>*.npz in directory, shards included."""
    vectors = {}
    for name in sorted(os.listdir(directory)):
        if name.startswith(prefix) and name.endswith(".npz") and ".partial" not in name:
            vectors.update(Cache(os.path.join(directory, name)).vectors)
    return vectors


def in_shard(key, shard):
    index, count = shard
    return int(hashlib.sha256(key.encode()).hexdigest()[:8], 16) % count == index


def image_jobs(sets):
    ground_truth = datasets.load_ground_truth()
    jobs = []
    if "coco" in sets:
        jobs += [(sample.key, sample.path) for sample in datasets.coco_samples(ground_truth)]
    if "openimages" in sets:
        ids = datasets.select_openimages(ground_truth)
        jobs += [(sample.key, sample.path) for sample in datasets.openimages_samples(ground_truth, ids)]
    if "coco-cn" in sets:
        # Test first, so that --limit keeps the split search is measured on.
        for split in ("test", "val"):
            images, _captions = datasets.coco_cn(split)
            jobs += [(f"coco-cn:{name}", path) for name, path in images]
    return jobs


def text_jobs():
    texts = set()
    for label in datasets.load_vocabulary():
        for word in [label["name"]] + label["synonyms"]:
            texts.update(template.format(word) for template in LABEL_TEMPLATES.values())
    for split in ("val", "test"):
        _images, captions = datasets.coco_cn(split)
        for found in captions.values():
            for caption in found["written"] + found["translated"]:
                texts.update(template.format(caption) for template in CAPTION_TEMPLATES.values())
    return sorted(texts)


def peak_rss_mib():
    return resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model", required=True)
    parser.add_argument("--tokens", type=int, default=70, choices=providers.VISION_TOKENS)
    parser.add_argument("--edge", type=int, default=512, help="thumbnail edge in pixels")
    parser.add_argument("--sets", default="coco,openimages,coco-cn")
    parser.add_argument("--limit", type=int, default=0, help="embed at most this many images per set (0: all)")
    parser.add_argument("--no-texts", action="store_true")
    parser.add_argument("--shard", default="0/1", help="I/N: embed only the I-th of N stable parts")
    parser.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"),
                        help="writable directory for the runtime's repacked weights; a local disk loads faster")
    args = parser.parse_args()
    shard = tuple(int(part) for part in args.shard.split("/"))
    suffix = "" if shard[1] == 1 else f"-{shard[0]}of{shard[1]}"

    started = time.monotonic()
    os.makedirs(args.weight_cache, exist_ok=True)
    provider = providers.MediaPipeProvider(args.model, MANIFEST, vision_tokens=args.tokens, cache_dir=args.weight_cache)
    print(f"model {provider.model} loaded in {time.monotonic() - started:.1f} s, peak RSS {peak_rss_mib():.0f} MiB", flush=True)
    directory = os.path.join(datasets.ROOT, "cache", f"{provider.model}-e{args.edge}")
    os.makedirs(directory, exist_ok=True)

    if not args.no_texts:
        done = load_vectors(directory, "texts")
        texts = Cache(os.path.join(directory, f"texts{suffix}.npz"))
        todo = [text for text in text_jobs() if text not in done and in_shard(text, shard)]
        started = time.monotonic()
        for text in todo:
            texts.vectors[text] = np.asarray(provider.embed_text(text), dtype=np.float32)
        if todo:
            texts.save()
            print(f"{len(todo)} texts in {time.monotonic() - started:.1f} s", flush=True)

    known = load_vectors(directory, "images")
    images = Cache(os.path.join(directory, f"images{suffix}.npz"))
    for name in args.sets.split(","):
        jobs = image_jobs({name})
        if args.limit:
            jobs = jobs[: args.limit]
        todo = [(key, path) for key, path in jobs if key not in known and in_shard(key, shard)]
        print(f"{name}: {len(jobs)} images, {len(todo)} to embed", flush=True)
        started, done, failed = time.monotonic(), 0, []
        for key, path in todo:
            try:
                vector = provider.embed_image(thumbnail(path, args.edge))
            except (OSError, providers.InvalidInput) as error:
                failed.append((key, str(error)))
                continue
            images.vectors[key] = np.asarray(vector, dtype=np.float32)
            done += 1
            if done % 500 == 0:
                images.save()
                elapsed = time.monotonic() - started
                print(f"  {done}/{len(todo)}  {elapsed / done:.3f} s/image  peak RSS {peak_rss_mib():.0f} MiB", flush=True)
        if done:
            images.save()
            print(f"  {done} images in {time.monotonic() - started:.0f} s ({(time.monotonic() - started) / done:.3f} s/image)", flush=True)
        for key, error in failed:
            print(f"  failed {key}: {error}", flush=True)
    provider.close()
    print(f"peak RSS {peak_rss_mib():.0f} MiB", flush=True)


if __name__ == "__main__":
    main()
