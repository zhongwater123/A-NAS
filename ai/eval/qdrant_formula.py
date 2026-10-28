"""Photo search's detector fusion as a Qdrant formula, against eval.hybrid_search.product.

Development only (docs/research/qdrant-retrieval-features.md, experiment 4).
internal/photos/search.go ranks a photo by its vector score plus weight x share
x (min over the query's enabled words of the best score among each word's
detector classes), and puts first the "detected" tier when every word reaches
its tier score; eval.hybrid_search.product computes the same in numpy. Here the
photos carry their best score per detector class as payload (`det.c<id>`,
absent when the detector saw nothing of it) and the rule becomes a formula over
a vector prefetch: Sum($score, Mult(weight x share, Min(Max(det.c...)...))),
plus 10 x Min(one condition per word: any of its classes >= its tier score).
Every variable the formula names gets the default 0, which is what an absent
class means to the Go code.

Candidates: every photo (exact), the best 500 by vector, and the best 500 plus
a second prefetch of the tier's photos. Reports the largest score difference,
recall of the reference's top 10/50, how many photos a prefetch would need
for the boost to change nothing, and latency.

    python -m eval.qdrant_formula [--captions 300]
"""

import argparse
import time

import numpy as np

from eval import datasets, qdrant_common as common
from eval.detect import load as load_detections
from eval.embed import QUERY_PROMPT, load_vectors
from eval.hybrid_search import Matcher, class_scores, coverage, product, word_enabled, word_verdict

TIER_BONUS = 10.0  # fused scores stay below 2, so the tier always comes first
LIMITS = (10, 50)
SMALL_PREFETCH = 500


def photos():
    """(keys, vectors, class score matrix) of the photos with both a vector and detections."""
    detections = load_detections(f"{common.DETECTIONS}/detections.npz")
    keys, matrix = common.image_vectors()
    rows = [row for row, key in enumerate(keys) if key in detections]
    keys = [keys[row] for row in rows]
    return keys, matrix[rows], class_scores(detections, keys)


def queries(matcher, captions):
    """[(text, vector)]: every vocabulary label name, then COCO-CN test captions, through the retrieval prompt."""
    texts = load_vectors(common.IMAGE_VECTORS, "texts")
    found = [label["name"] for label in datasets.load_vocabulary() if QUERY_PROMPT.format(label["name"]) in texts]
    _images, by_image = datasets.coco_cn("test")
    written = sorted({c for entry in by_image.values() for c in entry["written"] if QUERY_PROMPT.format(c) in texts})
    found += common.stable_sample(written, captions, "formula-captions")
    return [(text, np.asarray(texts[QUERY_PROMPT.format(text)], dtype=np.float32)) for text in found]


def formula_for(text, mentions, calibration):
    """(formula expression, defaults, tier filter or None) for a query, following product()."""
    from qdrant_client import models

    enabled = [(word, ids) for word, ids in mentions if word_enabled(calibration, ids)]
    share = coverage(text, enabled)
    names = set()
    parts = ["$score"]
    if enabled:
        best = []
        for _word, ids in enabled:
            variables = [f"det.c{cls}" for cls in sorted(ids)]
            names.update(variables)
            best.append(models.MaxExpression(max=variables))
        parts.append(models.MultExpression(mult=[calibration["weight"] * share, models.MinExpression(min=best)]))
    limits = [(word_verdict(calibration, ids) or {}).get("tierScore") for _word, ids in mentions]
    tier = None
    if mentions and len(enabled) == len(mentions) and share >= calibration["tierCoverage"] and None not in limits:
        words = []
        for (_word, ids), limit in zip(mentions, limits, strict=True):
            words.append(models.Filter(should=[models.FieldCondition(key=f"det.c{cls}", range=models.Range(gte=limit))
                                               for cls in sorted(ids)]))
        parts.append(models.MultExpression(mult=[TIER_BONUS, models.MinExpression(min=words)]))
        tier = models.Filter(must=words)
    return models.SumExpression(sum=parts), dict.fromkeys(names, 0.0), tier


def reference(text, mentions, scores, vector_scores, calibration):
    fused, tier = product(text, mentions, scores, vector_scores, calibration)
    return fused + (TIER_BONUS * tier if tier is not None else 0.0), fused, tier


def needed_prefetch(vector_scores, expected, share_weight, k):
    """Photos a vector prefetch must hold so that no boost can lift an outside photo into the top k."""
    kth = np.sort(expected)[-k]
    return int((vector_scores >= kth - share_weight).sum())


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--captions", type=int, default=300)
    args = parser.parse_args()
    from qdrant_client import models

    report = common.Report("formula")
    calibration = common.calibration()
    matcher = Matcher()
    keys, vectors, scores = photos()
    asked = queries(matcher, args.captions)
    report.put("data", {"photos": len(keys), "queries": len(asked), "calibrationModel": calibration["model"],
                        "weight": calibration["weight"], "tierCoverage": calibration["tierCoverage"]})
    entry = {}
    with common.Server("formula") as server:
        entry["start"] = server.start()
        qdrant = common.client()
        qdrant.create_collection("photos", vectors_config=models.VectorParams(size=vectors.shape[1],
                                                                             distance=models.Distance.DOT),
                                 strict_mode_config=models.StrictModeConfig(enabled=False))
        began = time.perf_counter()
        for start in range(0, len(keys), 512):
            stop = min(len(keys), start + 512)
            payloads = [{"key": keys[i], "det": {f"c{cls}": round(float(scores[i, cls]), 6)
                                                 for cls in np.flatnonzero(scores[i])}} for i in range(start, stop)]
            qdrant.upsert("photos", models.Batch(ids=list(range(start, stop)), vectors=vectors[start:stop].tolist(),
                                                 payloads=payloads), wait=True)
        entry["uploadS"] = round(time.perf_counter() - began, 2)
        entry["optimizeS"] = round(common.wait_indexed(qdrant, "photos"), 2)
        entry["collection"] = common.collection_facts(qdrant, "photos")
        entry["disk"] = server.disk("photos")
        stored = np.zeros_like(scores)
        for i in range(len(keys)):
            for cls in np.flatnonzero(scores[i]):
                stored[i, cls] = round(float(scores[i, cls]), 6)
        report.put("qdrant", entry)

        modes = {"all": None, f"vector{SMALL_PREFETCH}": SMALL_PREFETCH, f"vector{SMALL_PREFETCH}+tier": SMALL_PREFETCH}
        results = {mode: {"maxScoreDiff": 0.0, **{f"recall@{k}": [] for k in LIMITS}, "latencyMs": [],
                          "queriesChanged@10": 0} for mode in modes}
        plain_ms, numpy_ms, needed, tiered, with_words, missing_error = [], [], [], 0, 0, None
        for text, vector in asked:
            mentions = matcher.mentions(text)
            vector_scores = vectors @ vector
            with common.one_thread():
                began = time.perf_counter()
                expected, _fused, tier = reference(text, mentions, stored, vector_scores, calibration)
                numpy_ms.append((time.perf_counter() - began) * 1000)
            formula, defaults, tier_filter = formula_for(text, mentions, calibration)
            with_words += bool(defaults)
            tiered += tier_filter is not None
            enabled = [(w, ids) for w, ids in mentions if word_enabled(calibration, ids)]
            share_weight = calibration["weight"] * coverage(text, enabled)
            if tier is None:
                needed.append(needed_prefetch(vector_scores, expected, share_weight, max(LIMITS)))
            ms, _plain = common.timed_ms(qdrant.query_points, "photos", query=vector.tolist(), limit=max(LIMITS),
                                         search_params=models.SearchParams(exact=True), with_payload=False)
            plain_ms.append(ms)
            if defaults and missing_error is None:
                try:
                    qdrant.query_points("photos", prefetch=models.Prefetch(query=vector.tolist(), limit=len(keys)),
                                        query=models.FormulaQuery(formula=formula), limit=10, with_payload=False)
                    missing_error = "no error"
                except common.query_errors() as error:  # the error is the observation
                    missing_error = common.error_text(error)
            for mode, size in modes.items():
                prefetch = [models.Prefetch(query=vector.tolist(), limit=size or len(keys),
                                            params=models.SearchParams(exact=True))]
                query_defaults = dict(defaults)
                if mode.endswith("+tier") and tier_filter is not None:
                    prefetch.append(models.Prefetch(query=vector.tolist(), filter=tier_filter, limit=len(keys),
                                                    params=models.SearchParams(exact=True)))
                    # A photo from one prefetch only has no score from the other.
                    query_defaults.update({"$score[0]": -1.0, "$score[1]": -1.0})
                    score = models.MaxExpression(max=["$score[0]", "$score[1]"])
                    query_formula = models.SumExpression(sum=[score, *formula.sum[1:]])
                else:
                    query_formula = formula
                ms, response = common.timed_ms(qdrant.query_points, "photos", prefetch=prefetch, limit=max(LIMITS),
                                               query=models.FormulaQuery(formula=query_formula,
                                                                         defaults=query_defaults),
                                               with_payload=False)
                found = [(p.id, p.score) for p in response.points]
                outcome = results[mode]
                outcome["latencyMs"].append(ms)
                outcome["maxScoreDiff"] = max(outcome["maxScoreDiff"],
                                              max(abs(score - expected[point]) for point, score in found))
                ids = np.array([point for point, _score in found])
                for k in LIMITS:
                    outcome[f"recall@{k}"].append(common.recall(ids, expected, None, k))
                outcome["queriesChanged@10"] += common.recall(ids, expected, None, 10) < 1.0
        for outcome in results.values():
            outcome["latencyMs"] = common.latency(outcome["latencyMs"])
            outcome["maxScoreDiff"] = float(outcome["maxScoreDiff"])
            for k in LIMITS:
                values = outcome[f"recall@{k}"]
                outcome[f"recall@{k}"] = {"mean": round(float(np.mean(values)), 4), "min": round(float(min(values)), 4)}
        report.put("results", results)
        report.put("queries", {"withDetectorWords": with_words, "withTier": tiered,
                               "plainExactMs": common.latency(plain_ms), "numpyProductMs": common.latency(numpy_ms),
                               "prefetchNeededForTop50WithoutTier": {
                                   "median": float(np.median(needed)), "p95": float(np.percentile(needed, 95)),
                                   "max": int(max(needed))},
                               "missingVariableWithoutDefault": missing_error})
        entry["memoryAfterQueries"] = server.memory()
        report.put("qdrant", entry)


if __name__ == "__main__":
    main()
