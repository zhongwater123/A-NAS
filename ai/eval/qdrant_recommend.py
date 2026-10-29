"""Recommend with negatives and MMR in Qdrant, looked at on COCO val photos.

Development only (docs/research/qdrant-retrieval-features.md, experiment 5).
A qualitative check: the photos are COCO val 2017's cached vectors, and what
a result shows is read from COCO's own labels (its categories, the largest
first).

Recommend: a few "more like this, less like that" cases, each a positive
photo and a negative one picked by their COCO categories, compared across
the average_vector, best_score and sum_scores strategies: how many of the top
10 share the positive's subject and how many the negative's.

MMR: broad label queries ("狗", "食物", ...) through the retrieval prompt; the
plain top 10 against MMR at diversity 0.3/0.5/0.7 over 100 candidates: how
many distinct main subjects, how alike the ten are, how relevant. Qdrant's
order is checked against a numpy greedy MMR.

    python -m eval.qdrant_recommend
"""

import argparse
import collections
import json
import os

import numpy as np

from eval import datasets, qdrant_common as common
from eval.embed import QUERY_PROMPT, load_vectors

# (name, positive must have, positive must lack, negative must have)
CASES = (
    ("dog, not on furniture", {"dog"}, {"couch", "bed", "chair"}, {"dog", "couch"}),
    ("pizza without people", {"pizza"}, {"person"}, {"pizza", "person"}),
    ("cat, not by screens", {"cat"}, {"laptop", "tv", "keyboard"}, {"cat", "laptop"}),
    ("trains, not people at the station", {"train"}, {"person"}, {"train", "person"}),
    ("giraffe and zebra (two positives)", {"giraffe"}, {"person"}, None),
)
SECOND_POSITIVE = {"giraffe and zebra (two positives)": ({"zebra"}, {"person"})}
MMR_QUERIES = ("狗", "食物", "交通工具", "运动", "厨房", "动物")
DIVERSITY = (0.3, 0.5, 0.7)
CANDIDATES = 100


def coco_labels():
    """{photo key: [categories, largest area first]} for COCO val 2017."""
    with open(os.path.join(datasets.ROOT, datasets.COCO_ANNOTATIONS), encoding="utf-8") as file:
        data = json.load(file)
    names = {entry["id"]: entry["name"] for entry in data["categories"]}
    areas = collections.defaultdict(lambda: collections.defaultdict(float))
    for annotation in data["annotations"]:
        areas[f"coco:{annotation['image_id']}"][names[annotation["category_id"]]] += annotation["area"]
    return {key: [name for name, _area in sorted(found.items(), key=lambda item: -item[1])]
            for key, found in areas.items()}


def pick(keys, labels, must, lack, salt):
    """A photo that shows everything in must, the largest of it first, and nothing in lack."""
    chosen = [key for key in keys if must <= set(labels.get(key, ())) and labels[key][0] in must
              and not lack & set(labels.get(key, ()))]
    return common.stable_sample(chosen, 1, salt)[0]


def greedy_mmr(vectors, query, candidates, lam, limit):
    """Qdrant 1.20's MMR (lib/shard/src/query/mmr): best relevance first, then
    lambda x relevance - (1 - lambda) x the largest similarity to a chosen one."""
    relevance = vectors[candidates] @ query
    similarity = vectors[candidates] @ vectors[candidates].T
    chosen = [int(np.argmax(relevance))]
    while len(chosen) < min(limit, len(candidates)):
        closest = similarity[:, chosen].max(axis=1)
        score = lam * relevance - (1 - lam) * closest
        score[chosen] = -np.inf
        chosen.append(int(np.argmax(score)))
    return [int(candidates[index]) for index in chosen]


def describe(points, keys, labels):
    return [{"key": keys[point], "labels": labels.get(keys[point], [])[:4]} for point in points]


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.parse_args()
    from qdrant_client import models

    report = common.Report("recommend")
    labels = coco_labels()
    all_keys, all_vectors = common.image_vectors({"coco"})
    keys = [key for key in all_keys if key in labels]
    vectors = all_vectors[[all_keys.index(key) for key in keys]]
    texts = load_vectors(common.IMAGE_VECTORS, "texts")
    report.put("data", {"photos": len(keys)})
    with common.Server("recommend") as server:
        server.start()
        qdrant = common.client()
        qdrant.create_collection("photos", vectors_config=models.VectorParams(size=vectors.shape[1],
                                                                             distance=models.Distance.DOT))
        for start in range(0, len(keys), 512):
            stop = min(len(keys), start + 512)
            qdrant.upsert("photos", models.Batch(ids=list(range(start, stop)), vectors=vectors[start:stop].tolist()),
                          wait=True)
        common.wait_indexed(qdrant, "photos")

        cases = []
        for name, must, lack, negative_must in CASES:
            positive = [keys.index(pick(keys, labels, must, lack, name))]
            if name in SECOND_POSITIVE:
                positive.append(keys.index(pick(keys, labels, *SECOND_POSITIVE[name], name + "2")))
            negative = [] if negative_must is None else [keys.index(pick(keys, labels, negative_must, set(), name + "-"))]
            subject, unwanted = must, (negative_must - must) if negative_must else set()
            case = {"name": name, "positive": describe(positive, keys, labels),
                    "negative": describe(negative, keys, labels), "strategies": {}}
            for strategy in ("average_vector", "best_score", "sum_scores"):
                points = qdrant.query_points("photos", limit=10, query=models.RecommendQuery(
                    recommend=models.RecommendInput(positive=positive, negative=negative,
                                                    strategy=models.RecommendStrategy(strategy)))).points
                found = [p.id for p in points]
                case["strategies"][strategy] = {
                    "withSubject": sum(1 for p in found if subject & set(labels.get(keys[p], ()))),
                    "withNegativeSubject": sum(1 for p in found if unwanted & set(labels.get(keys[p], ()))),
                    "examplesReturned": sum(1 for p in found if p in positive + negative),
                    "top": describe(found, keys, labels)}
            cases.append(case)
            report.put("recommend", cases)

        diversity = []
        for word in MMR_QUERIES:
            text = QUERY_PROMPT.format(word)
            if text not in texts:
                continue
            query = np.asarray(texts[text], dtype=np.float32)
            plain = [p.id for p in qdrant.query_points("photos", query=query.tolist(), limit=10).points]
            candidates = np.argsort(-(vectors @ query), kind="stable")[:CANDIDATES]

            def summary(points, query=query):
                chosen = vectors[points]
                pairwise = chosen @ chosen.T
                return {"mainSubjects": len({labels[keys[p]][0] for p in points if labels.get(keys[p])}),
                        "meanPairwiseCosine": round(float(pairwise[np.triu_indices(len(points), 1)].mean()), 4),
                        "meanRelevance": round(float((chosen @ query).mean()), 4),
                        "top": [labels.get(keys[p], ["?"])[0] for p in points]}

            entry = {"query": word, "plain": summary(plain)}
            for amount in DIVERSITY:
                found = [p.id for p in qdrant.query_points("photos", limit=10, query=models.NearestQuery(
                    nearest=query.tolist(), mmr=models.Mmr(diversity=amount, candidates_limit=CANDIDATES))).points]
                same_as = {f"lambda={lam:.1f}": found == greedy_mmr(vectors, query, candidates, lam, 10)
                           for lam in (1 - amount, amount)}
                entry[f"mmr{amount}"] = {**summary(found), "matchesNumpy": same_as}
            diversity.append(entry)
            report.put("mmr", diversity)


if __name__ == "__main__":
    main()
