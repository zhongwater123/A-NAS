"""Measures which photos a search should show, from the vectors eval.embed cached.

A search that ranks every photo shows unrelated ones after the real
matches. This compares rules that keep only the matches, on the COCO test
half as a family-sized library (about 2,500 photos in their natural mix):

- each vocabulary label with COCO truth and enough positives is typed as a
  query, the way a user would ("task: search result | query: 猫");
- each rule keeps some of the photos; precision, recall and how often
  nothing is kept are averaged over the queries;
- COCO-CN captions (one right photo each) check that sentence queries keep
  their photo in a small set.

    python -m eval.search_cutoff --cache /datasets/cache/<model>-e512 [--report out.json]
"""

import argparse
import json

import numpy as np

from eval import datasets
from eval.calibrate import labelled
from eval.embed import QUERY_PROMPT, load_vectors

MIN_POSITIVES = 20


def rules():
    """Each rule maps a query's scores over the library to the photos it keeps."""
    found = {"rank only (top 500)": lambda s: np.argsort(-s)[:500]}
    for floor in (0.60, 0.62, 0.64, 0.66):
        found[f"floor {floor:.2f}"] = lambda s, f=floor: np.flatnonzero(s >= f)
    for gap in (0.02, 0.03, 0.04, 0.05, 0.06):
        found[f"top - {gap:.2f}"] = lambda s, g=gap: np.flatnonzero(s >= s.max() - g)
    for k in (2.0, 2.5, 3.0, 3.5):
        found[f"mean + {k:.1f} sd"] = lambda s, k=k: np.flatnonzero(s >= s.mean() + k * s.std())
    for floor in (0.62, 0.63, 0.64, 0.65):
        for gap in (0.04, 0.05):
            found[f"top - {gap:.2f}, floor {floor:.2f}"] = lambda s, g=gap, f=floor: np.flatnonzero(
                (s >= s.max() - g) & (s >= f))
    return found


def label_queries(cache, texts):
    images = load_vectors(cache, "images")
    vocabulary = datasets.load_vocabulary()
    samples, vectors, truth = labelled(images, vocabulary, datasets.load_ground_truth())
    library = np.array([sample.split == "test" and sample.key.startswith("coco:") for sample in samples])
    vectors, truth = vectors[library], truth[library]
    queries = []
    for column, label in enumerate(vocabulary):
        known = truth[:, column] >= 0
        if known.all() and truth[:, column].sum() >= MIN_POSITIVES:
            queries.append((label, texts[QUERY_PROMPT.format(label["name"])], truth[:, column] == 1))
    return vectors, queries


def evaluate(vectors, queries, calibration):
    report = {}
    for name, keep in rules().items():
        precision, recall, empty, kept = [], [], 0, []
        for _label, query, positive in queries:
            chosen = keep(vectors @ query)
            kept.append(len(chosen))
            if not len(chosen):
                empty += 1
                recall.append(0.0)
                continue
            hits = positive[chosen].sum()
            precision.append(hits / len(chosen))
            recall.append(hits / positive.sum())
        # The same query on a library without the thing: anything kept is wrong.
        absent = [len(keep(vectors[~positive] @ query)) for _label, query, positive in queries]
        report[name] = {"precision": float(np.mean(precision)), "recall": float(np.mean(recall)),
                        "emptyQueries": empty, "medianKept": float(np.median(kept)),
                        "absentShowsAny": float(np.mean([count > 0 for count in absent])), "absentMedianKept": float(np.median(absent))}
    # The calibrated thresholds, where a label has one.
    precision, recall = [], []
    for label, query, positive in queries:
        threshold = calibration["thresholds"].get(label["id"])
        if threshold is None:
            continue
        chosen = np.flatnonzero(vectors @ query >= threshold)
        if len(chosen):
            precision.append(positive[chosen].mean())
        recall.append(positive[chosen].sum() / positive.sum())
    report["calibrated label threshold"] = {"precision": float(np.mean(precision)), "recall": float(np.mean(recall)),
                                            "labels": len(recall)}
    return report


def captions(cache, texts):
    """COCO-CN test captions: whether each rule keeps the one right photo, and how many it keeps."""
    images = load_vectors(cache, "images")
    pairs, found = datasets.coco_cn("test")
    names = [name for name, _path in pairs if f"coco-cn:{name}" in images]
    library = np.stack([images[f"coco-cn:{name}"] for name in names])
    report = {}
    for name, keep in rules().items():
        included, sizes = [], []
        for row, image in enumerate(names):
            for caption in found[image]["written"]:
                chosen = keep(library @ texts[QUERY_PROMPT.format(caption)])
                included.append(row in set(chosen.tolist()))
                sizes.append(len(chosen))
        report[name] = {"keepsRightPhoto": float(np.mean(included)), "medianKept": float(np.median(sizes)), "captions": len(included)}
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--cache", required=True)
    parser.add_argument("--report")
    args = parser.parse_args()
    texts = load_vectors(args.cache, "texts")
    with open(datasets.os.path.join(datasets.HERE, "labels", "v1.calibration.json"), encoding="utf-8") as file:
        calibration = json.load(file)
    vectors, queries = label_queries(args.cache, texts)
    print(f"{len(queries)} label queries over {len(vectors)} COCO test photos")
    labels = evaluate(vectors, queries, calibration)
    for name, value in labels.items():
        print(f"{name:34s} " + "  ".join(f"{key} {value[key]:.3f}" if isinstance(value[key], float) else f"{key} {value[key]}" for key in value))
    sentences = captions(args.cache, texts)
    print("COCO-CN written captions:")
    for name, value in sentences.items():
        print(f"{name:34s} keeps right photo {value['keepsRightPhoto']:.3f}  median kept {value['medianKept']:.0f}")
    if args.report:
        with open(args.report, "w", encoding="utf-8") as file:
            json.dump({"labels": labels, "captions": sentences}, file, ensure_ascii=False, indent=2)


if __name__ == "__main__":
    main()
