"""Prepares a fixed subset of COCO 2017 train for calibrating AI clusters.

Development only. The 5,000 val images leave rare labels and most scenes with
too few photos to tell whether a cluster is right
(docs/research/photo-ai-label-calibration.md#ai-聚合的严格校准). This picks
--count train images by a hash of their ID, writes their instance and
COCO-Stuff annotations in a compact form (image, class, share of the photo;
no masks) and downloads the images. The two source files are streamed rather
than loaded: instances_train2017.json from the local
annotations_trainval2017.zip, and stuff_train2017.json read out of
cocodataset.org's stuff archive with HTTP range requests, so neither of the
two large JSON files is held in memory.

It runs on the development host, outside the evaluation image, and needs
ijson and remotezip:

    ANAS_DATASETS=D:/A-NAS-datasets python -m eval.coco_train --count 20000

The images use eval.fetch's retrying download; the subset's annotations land
in coco/train-subset.json and coco-stuff/stuff_train-subset.json.
"""

import argparse
import concurrent.futures
import hashlib
import json
import os
import zipfile

import ijson
from remotezip import RemoteZip

from eval import datasets
from eval.fetch import download

ANNOTATIONS_ZIP = os.path.join("coco", "annotations_trainval2017.zip")
STUFF_ZIP = "http://images.cocodataset.org/annotations/stuff_annotations_trainval2017.zip"


def order(image_id):
    return hashlib.sha256(str(image_id).encode()).hexdigest()


def compact(stream, chosen):
    """Categories by ID and [image ID, category ID, area] of the chosen images."""
    categories, annotations = {}, []
    for prefix, event, value in ijson.parse(stream):
        if prefix == "categories.item" and event == "start_map":
            current = {}
        elif prefix in ("categories.item.id", "categories.item.name"):
            current[prefix.rsplit(".", 1)[1]] = value
        elif prefix == "categories.item" and event == "end_map":
            categories[current["id"]] = current["name"]
        elif prefix == "annotations.item" and event == "start_map":
            current = {}
        elif prefix in ("annotations.item.image_id", "annotations.item.category_id", "annotations.item.area"):
            current[prefix.rsplit(".", 1)[1]] = value
        elif prefix == "annotations.item" and event == "end_map" and current["image_id"] in chosen:
            annotations.append([current["image_id"], current["category_id"], float(current["area"])])
    return categories, annotations


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--count", type=int, default=20000)
    parser.add_argument("--workers", type=int, default=16)
    args = parser.parse_args()
    root = datasets.ROOT

    with zipfile.ZipFile(os.path.join(root, ANNOTATIONS_ZIP)) as archive:
        with archive.open("annotations/instances_train2017.json") as stream:
            images = [{"id": item["id"], "file_name": item["file_name"], "width": item["width"], "height": item["height"]}
                      for item in ijson.items(stream, "images.item")]
        images = sorted(images, key=lambda item: order(item["id"]))[: args.count]
        chosen = {item["id"] for item in images}
        with archive.open("annotations/instances_train2017.json") as stream:
            categories, annotations = compact(stream, chosen)
    with open(os.path.join(root, datasets.COCO_TRAIN), "w", encoding="utf-8") as file:
        json.dump({"images": images, "categories": categories,
                   "annotations": [[image, categories[category], area] for image, category, area in annotations]}, file)
    print(f"{len(images)} train images, {len(annotations)} instance annotations", flush=True)

    with RemoteZip(STUFF_ZIP) as archive, archive.open("annotations/stuff_train2017.json") as stream:
        categories, annotations = compact(stream, chosen)
    with open(os.path.join(root, datasets.COCO_STUFF_TRAIN), "w", encoding="utf-8") as file:
        json.dump({"annotations": [[image, categories[category], area] for image, category, area in annotations]}, file)
    print(f"{len(annotations)} stuff annotations", flush=True)

    folder = os.path.join(root, datasets.COCO_TRAIN_IMAGES)
    os.makedirs(folder, exist_ok=True)
    missing = [item["file_name"] for item in images if not os.path.exists(os.path.join(folder, item["file_name"]))]
    print(f"{len(missing)} images to download", flush=True)
    done = 0
    with concurrent.futures.ThreadPoolExecutor(args.workers) as pool:
        for _ in pool.map(lambda name: download(f"http://images.cocodataset.org/train2017/{name}", os.path.join(folder, name)), missing):
            done += 1
            if done % 1000 == 0:
                print(f"  {done}/{len(missing)}", flush=True)
    print(f"downloaded {done} images", flush=True)


if __name__ == "__main__":
    main()
