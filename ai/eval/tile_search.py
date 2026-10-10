"""Measures whether tile vectors find small things the whole-photo vector misses.

eval.embed --tiles N caches the vectors of an N x N grid of tiles beside the
whole-photo vectors. A photo's score for a query then combines its whole and
tile scores; each way of combining them is compared with the whole-photo
score alone, on the photos that have every tile:

- the COCO test half as a family-sized library: each vocabulary label mapped
  to COCO categories with enough positives is typed as a query, the way a
  user would ("task: search result | query: 手机"); recall within the top R
  (R = the label's positives) and average precision, overall and by how much
  of the frame the label's largest instance in a photo covers;
- the rule search uses to mark the closest results (within 0.04 of the best):
  its precision and recall, and how many photos it still marks when the
  library lacks the thing;
- COCO-CN test captions (one right photo each): R@1/5/10.

    python -m eval.tile_search --cache /datasets/cache/<model>-e512 \\
        --tiles /datasets/cache/<model>-e512-tiles2 [--report out.json]

It runs on whatever part of the tiles is cached, so it reports interim
results while eval.embed is still going.
"""

import argparse
import collections
import json
import os

import numpy as np
from sklearn.metrics import average_precision_score

from eval import datasets
from eval.embed import QUERY_PROMPT, load_vectors

MIN_POSITIVES = 20
GAP = 0.04  # internal/photos/search.go closestGap
SIZES = (("<1%", 0.0, 0.01), ("1-5%", 0.01, 0.05), ("5-20%", 0.05, 0.2), (">=20%", 0.2, 1.01))
WATCH = ("手机", "杯子", "瓶子", "笔记本电脑", "键盘", "鼠标", "时钟", "猫", "狗")


def combiners():
    """Each maps whole-photo scores (n) and tile scores (n x tiles) to photo scores."""
    found = {
        "whole only": lambda whole, tiles: whole,
        "max": lambda whole, tiles: np.maximum(whole, tiles.max(axis=1)),
    }
    for penalty in (0.01, 0.02, 0.03, 0.05):
        found[f"max, tiles - {penalty:.2f}"] = lambda whole, tiles, p=penalty: np.maximum(whole, tiles.max(axis=1) - p)
    found["mean of whole and best tile"] = lambda whole, tiles: (whole + tiles.max(axis=1)) / 2
    return found


def load_library(whole_cache, tile_cache, prefix):
    """Keys under prefix with a whole vector and every tile: keys, whole (n x d), tiles (n x tiles x d)."""
    whole = load_vectors(whole_cache, "images")
    tiles = load_vectors(tile_cache, "images")
    suffixes = sorted({key.split("#", 1)[1] for key in tiles if "#" in key})
    keys = sorted({key.split("#", 1)[0] for key in tiles if key.startswith(prefix)})
    keys = [key for key in keys if key in whole and all(f"{key}#{suffix}" in tiles for suffix in suffixes)]
    if not keys:
        return [], None, None
    return (keys, np.stack([whole[key] for key in keys]),
            np.stack([[tiles[f"{key}#{suffix}"] for suffix in suffixes] for key in keys]))


def coco_largest(root=datasets.ROOT):
    """{(image key, category): share of the frame its largest instance covers}."""
    with open(os.path.join(root, datasets.COCO_ANNOTATIONS), encoding="utf-8") as file:
        data = json.load(file)
    category = {entry["id"]: entry["name"] for entry in data["categories"]}
    frame = {entry["id"]: entry["width"] * entry["height"] for entry in data["images"]}
    largest = collections.defaultdict(float)
    for annotation in data["annotations"]:
        key = (f"coco:{annotation['image_id']}", category[annotation["category_id"]])
        largest[key] = max(largest[key], annotation["area"] / frame[annotation["image_id"]])
    return largest


def label_queries(keys, texts, min_positives=MIN_POSITIVES):
    """Labels mapped to COCO categories: (label, query vector, positive mask, size of each positive)."""
    ground_truth = datasets.load_ground_truth()
    largest = coco_largest()
    samples = {sample.key: sample for sample in datasets.coco_samples(ground_truth)}
    queries = []
    for label in datasets.load_vocabulary():
        categories = ground_truth.get(label["id"], {}).get("coco")
        if not categories:
            continue
        positive = np.array([label["id"] in samples[key].positive for key in keys])
        if positive.sum() < min_positives:
            continue
        size = np.array([max(largest[(key, name)] for name in categories) for key in keys])
        queries.append((label, texts[QUERY_PROMPT.format(label["name"])], positive, size))
    return queries


def measure(scores, positive, size):
    count = int(positive.sum())
    top = np.zeros(len(scores), dtype=bool)
    top[np.argsort(-scores)[:count]] = True
    by_size = {}
    for name, low, high in SIZES:
        in_bucket = positive & (size >= low) & (size < high)
        by_size[name] = (int((top & in_bucket).sum()), int(in_bucket.sum()))
    closest = scores >= scores.max() - GAP
    absent = scores[~positive]
    return {
        "recallAtR": float((top & positive).sum() / count),
        "ap": float(average_precision_score(positive, scores)),
        "bySize": by_size,
        "closestPrecision": float(positive[closest].mean()),
        "closestRecall": float((closest & positive).sum() / count),
        "absentClosestKept": int((absent >= absent.max() - GAP).sum()),
    }


def labels_report(whole_cache, tile_cache, texts, min_positives=MIN_POSITIVES):
    keys, whole, tiles = load_library(whole_cache, tile_cache, "coco:")
    keys_test = [i for i, key in enumerate(keys) if datasets.Sample(key, "").split == "test"]
    if not keys_test:
        return None
    keys = [keys[i] for i in keys_test]
    whole, tiles = whole[keys_test], tiles[keys_test]
    queries = label_queries(keys, texts, min_positives)
    if not queries:
        return None
    report = {"photos": len(keys), "queries": len(queries), "combiners": {}}
    for name, combine in combiners().items():
        per_label, pooled = {}, collections.defaultdict(lambda: [0, 0])
        for label, query, positive, size in queries:
            result = measure(combine(whole @ query, tiles @ query), positive, size)
            per_label[label["name"]] = result
            for bucket, (found, total) in result["bySize"].items():
                pooled[bucket][0] += found
                pooled[bucket][1] += total
        values = list(per_label.values())
        report["combiners"][name] = {
            "recallAtR": float(np.mean([value["recallAtR"] for value in values])),
            "map": float(np.mean([value["ap"] for value in values])),
            "recallAtRBySize": {bucket: {"recall": found / total if total else None, "positives": total}
                                for bucket, (found, total) in pooled.items()},
            "closestPrecision": float(np.mean([value["closestPrecision"] for value in values])),
            "closestRecall": float(np.mean([value["closestRecall"] for value in values])),
            "absentClosestKeptMedian": float(np.median([value["absentClosestKept"] for value in values])),
            "labels": per_label,
        }
    return report


def captions_report(whole_cache, tile_cache, texts):
    keys, whole, tiles = load_library(whole_cache, tile_cache, "coco-cn:")
    if not keys:
        return None
    _pairs, found = datasets.coco_cn("test")
    report = {"photos": len(keys)}
    for name, combine in combiners().items():
        ranks = []
        for row, key in enumerate(keys):
            for caption in found[key.split(":", 1)[1]]["written"]:
                query = texts[QUERY_PROMPT.format(caption)]
                scores = combine(whole @ query, tiles @ query)
                ranks.append(int((scores > scores[row]).sum()))
        ranks = np.array(ranks)
        report[name] = {"captions": len(ranks), **{f"R@{k}": float((ranks < k).mean()) for k in (1, 5, 10)}}
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--cache", required=True, help="the whole-photo cache, which also holds the text vectors")
    parser.add_argument("--tiles", required=True, help="the tile cache eval.embed --tiles wrote")
    parser.add_argument("--min-positives", type=int, default=MIN_POSITIVES, help="fewest positives a label query needs")
    parser.add_argument("--report")
    args = parser.parse_args()
    texts = load_vectors(args.cache, "texts")
    labels = labels_report(args.cache, args.tiles, texts, args.min_positives)
    if labels:
        print(f"{labels['queries']} label queries over {labels['photos']} COCO test photos with every tile")
        for name, value in labels["combiners"].items():
            sizes = "  ".join(f"{bucket} {entry['recall']:.3f}" for bucket, entry in value["recallAtRBySize"].items()
                              if entry["recall"] is not None)
            print(f"  {name:30s} R@R {value['recallAtR']:.3f}  mAP {value['map']:.3f}  [{sizes}]  "
                  f"closest P {value['closestPrecision']:.3f} R {value['closestRecall']:.3f}  "
                  f"absent kept {value['absentClosestKeptMedian']:.0f}")
        positives = {bucket: entry["positives"] for bucket, entry in labels["combiners"]["whole only"]["recallAtRBySize"].items()}
        print(f"  positives by size: {positives}")
        for label in WATCH:
            if label in labels["combiners"]["whole only"]["labels"]:
                line = []
                for name in ("whole only", "max", "max, tiles - 0.02"):
                    small = labels["combiners"][name]["labels"][label]["bySize"]["<1%"]
                    line.append(f"{name}: <1% {small[0]}/{small[1]}")
                print(f"  {label}: " + "  ".join(line))
    captions = captions_report(args.cache, args.tiles, texts)
    if captions:
        print(f"COCO-CN written captions over {captions['photos']} test photos with every tile")
        for name, value in captions.items():
            if name != "photos":
                print(f"  {name:30s} R@1 {value['R@1']:.3f}  R@5 {value['R@5']:.3f}  R@10 {value['R@10']:.3f}")
    if args.report:
        with open(args.report, "w", encoding="utf-8") as file:
            json.dump({"labels": labels, "captions": captions}, file, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
