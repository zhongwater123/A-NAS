"""Measures whether detected objects help finding similar photos.

Development only (docs/architecture/photo-objects.md#以图搜图). Similar photos
are ranked by the cosine of their image vectors; this checks whether adding
the overlap of the objects the detector found in both photos helps. On the
COCO test half, each photo is a query; another photo counts as similar when
it holds the query's main thing, the category whose largest instance covers
the most of the query photo. Mean average precision and precision in the
first 10, overall and by how much of the frame the main thing covers.

    python -m eval.image_search --vectors /datasets/cache/<model>-e512 \\
        --detections /datasets/cache/<detector> [--report out.json]
"""

import argparse
import collections
import json
import os

import numpy as np
from sklearn.metrics import average_precision_score

from eval import datasets
from eval.detect import load as load_detections
from eval.embed import load_vectors
from eval.hybrid_search import class_scores

MIN_OBJECT_SCORE = 0.3  # objects that take part in the overlap
WEIGHTS = (0.05, 0.1, 0.2)
PERSON = 1
SIZES = (("<5%", 0.0, 0.05), (">=5%", 0.05, 1.01))


def main_things(keys):
    """{image key: (main COCO category, share of the frame), all its categories}."""
    with open(os.path.join(datasets.ROOT, datasets.COCO_ANNOTATIONS), encoding="utf-8") as file:
        data = json.load(file)
    category = {entry["id"]: entry["name"] for entry in data["categories"]}
    frame = {f"coco:{entry['id']}": entry["width"] * entry["height"] for entry in data["images"]}
    largest = collections.defaultdict(dict)
    for annotation in data["annotations"]:
        key = f"coco:{annotation['image_id']}"
        name = category[annotation["category_id"]]
        largest[key][name] = max(largest[key].get(name, 0.0), annotation["area"] / frame[key])
    found = {}
    for key in keys:
        if largest.get(key):
            name, share = max(largest[key].items(), key=lambda item: item[1])
            found[key] = (name, share, set(largest[key]))
    return found


def overlap(scores, query, without_person):
    """Weighted Jaccard of the objects in each photo and in the query photo."""
    present = np.where(scores >= MIN_OBJECT_SCORE, scores, 0.0)
    if without_person:
        present[:, PERSON] = 0.0
    q = present[query]
    union = np.maximum(present, q).sum(axis=1)
    return np.where(union > 0, np.minimum(present, q).sum(axis=1) / np.maximum(union, 1e-9), 0.0)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--vectors", required=True)
    parser.add_argument("--detections", required=True)
    parser.add_argument("--report")
    args = parser.parse_args()
    images = load_vectors(args.vectors, "images")
    detections = load_detections(os.path.join(args.detections, "detections.npz"))
    keys = sorted(k for k in images if k.startswith("coco:") and k in detections
                  and datasets.Sample(k, "").split == "test")
    things = main_things(keys)
    keys = [k for k in keys if k in things]
    vectors = np.stack([images[k] for k in keys])
    scores = class_scores(detections, keys)
    cosine = vectors @ vectors.T
    rankings = {"cosine": lambda q: cosine[q]}
    for without_person in (False, True):
        for weight in WEIGHTS:
            name = f"cosine + {weight:.2f} x overlap{' (no person)' if without_person else ''}"
            rankings[name] = lambda q, w=weight, p=without_person: cosine[q] + w * overlap(scores, q, p)
    results = collections.defaultdict(lambda: collections.defaultdict(list))
    for q, key in enumerate(keys):
        main_name, share, _all = things[key]
        truth = np.array([main_name in things[k][2] for k in keys])
        truth[q] = False
        if truth.sum() < 5:
            continue
        others = np.ones(len(keys), dtype=bool)
        others[q] = False
        size = next(name for name, low, high in SIZES if low <= share < high)
        for name, rank in rankings.items():
            score = rank(q)[others]
            y = truth[others]
            top = np.argsort(-score, kind="stable")[:10]
            for bucket in ("all", size):
                results[name][f"{bucket} AP"].append(average_precision_score(y, score))
                results[name][f"{bucket} P@10"].append(y[top].mean())
    report = {"photos": len(keys), "queries": len(results["cosine"]["all AP"])}
    for name, metrics in results.items():
        report[name] = {metric: round(float(np.mean(values)), 4) for metric, values in sorted(metrics.items())}
    print(json.dumps(report, ensure_ascii=False, indent=1))
    if args.report:
        with open(args.report, "w", encoding="utf-8") as file:
            json.dump(report, file, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
