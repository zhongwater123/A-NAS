"""Measures whether detector boxes make search find what image vectors miss.

Development only (docs/research/photo-ai-object-detection.md). It combines the
photo vectors that eval.embed cached with the D-FINE Objects365 boxes that
eval.detect cached, and compares ways of ranking:

- the COCO test half as a family-sized library: each vocabulary label with COCO
  truth and enough positives is typed as a query, the way a user would
  ("task: search result | query: 手机"); the query maps to detector classes
  through the Chinese table (internal/photos/objects/v1.json). Recall within the top R
  (R = the label's positives) and average precision, overall and by how much
  of the frame the label's largest instance covers; for the first tier, its
  precision and recall, and how many photos it still shows when the library
  lacks the thing;
- COCO-CN test captions (one right photo each): R@1/5/10, so that hybrid
  ranking does not hurt sentence queries;
- Open Images validation, for the detector classes beyond COCO's 80: each
  class whose name Open Images verifies on at least 20 photos either way is
  typed as a query by its Chinese name; average precision of each ranking on
  the verified photos, and the detector's precision and recall at a score.
  Open Images negatives were proposed by a model and rejected by people, so
  they are harder than a family library's.

The Chinese class names are encoded once with the photo vectors' model
(--embedder) into the detections directory.

    python -m eval.hybrid_search --vectors /datasets/cache/<model>-e512 \\
        --detections /datasets/cache/<detector> [--embedder <.litertlm>] [--report out.json]
"""

import argparse
import collections
import json
import os

import numpy as np
from sklearn.metrics import average_precision_score

from eval import datasets
from eval.detect import load as load_detections
from eval.embed import MANIFEST, QUERY_PROMPT, Cache, load_vectors

TABLE = os.path.join(datasets.REPO, "internal", "photos", "objects", "v1.json")
MIN_POSITIVES = 20
GAP = 0.04  # internal/photos/search.go closestGap
SIZES = (("<1%", 0.0, 0.01), ("1-5%", 0.01, 0.05), ("5-20%", 0.05, 0.2), (">=20%", 0.2, 1.01))
THRESHOLDS = (0.3, 0.4, 0.5, 0.6)
TARGET_PRECISION = 0.9  # per-class thresholds from the calibration half
RRF_K = 60
# Objects365 names that Open Images spells differently.
OPENIMAGES_ALIASES = {
    "Cell Phone": ["Mobile phone"], "Potted Plant": ["Houseplant"], "Cow": ["Cattle"], "Other Fish": ["Fish"],
    "Trash bin Can": ["Waste container"], "Donut": ["Doughnut"], "Remote": ["Remote control"],
    "Microwave": ["Microwave oven"], "Head Phone": ["Headphones"], "Dinning Table": ["Kitchen & dining room table"],
    "Monitor/TV": ["Television"], "Stroller": ["Baby carriage"], "Tea pot": ["Teapot"],
    "Hot-air balloon": ["Hot air balloon"], "Kiwi fruit": ["Kiwifruit"], "Coffee Machine": ["Coffeemaker"],
    "Volleyball": ["Volleyball (Ball)"],
}


class Matcher:
    """Finds the detector classes a query names, the longest word first (热狗 is not 狗)."""

    def __init__(self, path=TABLE):
        with open(path, encoding="utf-8") as file:
            table = json.load(file)
        words = collections.defaultdict(set)
        for entry in table["classes"]:
            for word in [entry["zh"]] + entry["synonyms"]:
                words[word.lower()].add(entry["id"])
        for word, ids in table["groups"].items():
            words[word.lower()].update(ids)
        # Ignored words are consumed whole and name nothing (马路 is not a horse).
        for word in table.get("ignore", []):
            words[word.lower()] = set()
        self.words = sorted(words.items(), key=lambda item: -len(item[0]))
        self.names = {entry["id"]: entry["zh"] for entry in table["classes"]}

    def mentions(self, text):
        """[(word, class ids)] in order of appearance."""
        text, found, position = text.lower(), [], 0
        while position < len(text):
            for word, ids in self.words:
                if text.startswith(word, position):
                    if ids:
                        found.append((word, frozenset(ids)))
                    position += len(word)
                    break
            else:
                position += 1
        return found


def class_scores(detections, keys):
    """keys x 366 array: each class's best box score in each image."""
    scores = np.zeros((len(keys), 366), dtype=np.float32)
    for row, key in enumerate(keys):
        classes, found, _boxes = detections.get(key, ((), (), ()))
        for cls, score in zip(classes, found):
            scores[row, cls] = max(scores[row, cls], score)
    return scores


def mention_score(scores, mentions):
    """An image's detector score for a query: every mention must be present (min over mentions of the best class)."""
    if not mentions:
        return None
    return np.min(np.stack([scores[:, sorted(ids)].max(axis=1) for _word, ids in mentions]), axis=0)


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


def rank_order(score, tiebreak):
    """Indices best first: by score, then by the tiebreak score."""
    return np.lexsort((-tiebreak, -score))


def recall_at(order, truth, r):
    return truth[order[:r]].sum() / max(1, truth.sum())


def tier_rank(vector, detector, threshold):
    """First tier: photos the detector finds the thing in, by vector score; then the rest by vector score."""
    tier = (detector >= threshold).astype(np.float32)
    return rank_order(tier, vector), tier.astype(bool)


def rrf_rank(vector, detector):
    ranks = np.empty((2, len(vector)))
    for row, score in enumerate((vector, detector)):
        ranks[row, np.argsort(-score, kind="stable")] = np.arange(len(score))
    fused = (1 / (RRF_K + 1 + ranks)).sum(axis=0)
    return np.argsort(-fused, kind="stable")


SOFT_WEIGHTS = (0.02, 0.05, 0.1, 0.2)
COVERAGE_WEIGHTS = (0.1, 0.2, 0.5)
FUSION_WEIGHT = 0.2  # the coverage weight that ranked best on both COCO halves
TIER_SCORE = 0.6  # the "detected" group: about 92% right on COCO in the study
TIER_COVERAGE = 0.6  # a query is about a thing when its class words make up this much
CALIBRATION = os.path.join(datasets.REPO, "internal", "photos", "objects", "v1.calibration.json")
CALIBRATION_ABOUT = ("Written by ai/eval/hybrid_search.py --write-calibration; do not edit. Search adds weight x coverage x "
                     "detector score for words whose classes are all on the fusion list, and puts the photos with a "
                     "detector score of at least tierScore first when the query is at least tierCoverage words on the "
                     "tier list (docs/architecture/photo-objects.md). Only boxes of this model count.")
PUNCTUATION = set(" \t，。、！？；：,.!?;:\"'“”‘’（）()")


def coverage(text, mentions):
    """How much of the query the class words make up: 1 for 手机, little for a sentence."""
    length = sum(1 for char in text if char not in PUNCTUATION)
    return min(1.0, sum(len(word) for word, _ids in mentions) / max(1, length))


def hybrid_rankings(vector, detector, detector_any, share):
    """The ways of adding the detector to the vector ranking that work without per-query tuning."""
    rankings = {}
    for weight in SOFT_WEIGHTS:
        rankings[f"soft {weight:.2f}"] = np.argsort(-(vector + weight * detector), kind="stable")
    for weight in COVERAGE_WEIGHTS:
        rankings[f"coverage x {weight:.1f}"] = np.argsort(-(vector + weight * share * detector), kind="stable")
    rankings["tiers @0.5, any class"] = tier_rank(vector, detector_any, 0.5)[0]
    rankings["coverage>=0.6 ? tiers @0.5 : soft 0.02"] = (
        tier_rank(vector, detector, 0.5)[0] if share >= 0.6 else rankings["soft 0.02"])
    return rankings


RANKINGS_FOR_CAPTIONS = (
    "rrf", *[f"tiers @{t:.1f}" for t in THRESHOLDS], *[f"soft {w:.2f}" for w in SOFT_WEIGHTS],
    *[f"coverage x {w:.1f}" for w in COVERAGE_WEIGHTS], "tiers @0.5, any class",
    "coverage>=0.6 ? tiers @0.5 : soft 0.02")


def mention_any_score(scores, mentions):
    """Like mention_score, but any one mention suffices."""
    return np.max(np.stack([scores[:, sorted(ids)].max(axis=1) for _word, ids in mentions]), axis=0)


def product(text, mentions, scores, vector, calibration):
    """The photo service's rule (internal/photos/search.go): fused scores, and the
    "detected" tier when the query is mostly words on the tier list, else None.

    Only enabled words count: their classes are all on the fusion list and the
    word was not found harmful. They make up the coverage and the detector
    score; other words are matched but add nothing.
    """
    enabled = [(word, ids) for word, ids in mentions
               if word not in calibration["disabledWords"] and set(ids) <= calibration["fusion"]]
    share = coverage(text, enabled)
    detector = mention_score(scores, enabled) if enabled else np.zeros(len(vector))
    fused = vector + calibration["weight"] * share * detector
    tiered = (bool(mentions) and len(enabled) == len(mentions) and share >= calibration["tierCoverage"]
              and all(set(ids) <= calibration["tier"] for _word, ids in mentions))
    return fused, (detector >= calibration["tierScore"]) if tiered else None


def product_order(fused, tier):
    if tier is None:
        return np.argsort(-fused, kind="stable")
    return rank_order(tier.astype(np.float32), fused)


def product_first_group(fused, tier):
    """What the photo service marks as the closest group: the tier, or within GAP of the best."""
    if tier is not None and tier.any():
        return tier
    return fused >= fused.max() - GAP


def derive_lists(label_halves, oi_classes, table_path=TABLE, min_delta=-0.02, tier_precision=0.85, tier_recall=0.5):
    """The fusion and tier lists from the per-query results.

    A word is fine for fusion when fusing does not cost more than min_delta of
    average precision, on average over what measured it (COCO, Open Images);
    fine for the tier when the "detected" group at the tier score is at least
    tier_precision right and finds at least tier_recall of the true photos,
    wherever it was measured. A
    class is on a list when a word naming at most three classes, it among them,
    is fine and none is not; classes whose meaning is a guess stay off. Words
    found harmful are listed so that the matcher still consumes them.
    """
    with open(table_path, encoding="utf-8") as file:
        noted = {entry["id"] for entry in json.load(file)["classes"] if entry.get("note")}
    words = collections.defaultdict(lambda: {"classes": set(), "deltas": [], "tier": []})
    for per_query in label_halves:
        for entry in per_query:
            if "coverage x 0.2" not in entry:
                continue
            word = words[entry["label"]]
            word["classes"] = set(entry["classIds"])
            word["deltas"].append(entry["coverage x 0.2"]["AP"] - entry["vector"]["AP"])
            tier = entry["tiers @0.6"]["tier"]
            word["tier"].append(tier["precision"] is not None and tier["precision"] >= tier_precision
                                and tier["recall"] >= tier_recall)
    for entry in oi_classes:
        word = words[entry["zh"]]
        word["classes"] = set(entry["classIds"])
        word["deltas"].append(entry["fusedAP"] - entry["vectorAP"])
        at = entry["detector@0.6"]
        word["tier"].append(at["precision"] is not None and at["precision"] >= tier_precision and at["recall"] >= tier_recall)
    fine = {name: float(np.mean(word["deltas"])) >= min_delta for name, word in words.items()}
    tier_fine = {name: fine[name] and all(word["tier"]) for name, word in words.items()}
    fusion, tier = set(), set()
    for cls in range(1, 366):
        if cls in noted:
            continue
        naming = [name for name, word in words.items() if cls in word["classes"]]
        specific = [name for name in naming if len(words[name]["classes"]) <= 3]
        if any(fine[name] for name in specific) and all(fine[name] for name in specific):
            fusion.add(cls)
            if any(tier_fine[name] for name in specific) and all(tier_fine[name] for name in specific):
                tier.add(cls)
    disabled = sorted(name for name, ok in fine.items() if not ok)
    return {"fusion": fusion, "tier": tier, "disabledWords": set(disabled)}, {
        name: {"classes": sorted(word["classes"]), "deltaAP": round(float(np.mean(word["deltas"])), 3),
               "fusion": fine[name], "tier": tier_fine[name]} for name, word in sorted(words.items())}


def calibrated_threshold(detector, truth, target=TARGET_PRECISION):
    """Lowest score whose photos at or above it are at least target precise; None when none is."""
    order = np.argsort(-detector, kind="stable")
    hits = np.cumsum(truth[order])
    precision = hits / np.arange(1, len(order) + 1)
    good = np.flatnonzero((precision >= target) & (detector[order] > 0))
    return float(detector[order][good[-1]]) if len(good) else None


def label_report(vectors, detections, matcher, half="test", calibration=None):
    """Ranks on one COCO half; per-class thresholds come from the other. With
    calibration, "product" is the photo service's ranking."""
    other = "calibration" if half == "test" else "test"
    vocabulary = datasets.load_vocabulary()
    ground_truth = datasets.load_ground_truth()
    images = load_vectors(vectors, "images")
    texts = load_vectors(vectors, "texts")
    samples = [s for s in datasets.coco_samples(ground_truth) if s.key in images and s.key in detections]
    halves = {name: [s for s in samples if s.split == name] for name in ("test", "calibration")}
    largest = coco_largest()
    library = {}
    for name, chosen in halves.items():
        keys = [s.key for s in chosen]
        library[name] = (chosen, np.stack([images[k] for k in keys]), class_scores(detections, keys))

    per_query = []
    for label in vocabulary:
        coco = ground_truth.get(label["id"], {}).get("coco")
        text = QUERY_PROMPT.format(label["name"])
        test_samples, test_vectors, test_classes = library[half]
        truth = np.array([label["id"] in s.positive for s in test_samples])
        if not coco or truth.sum() < MIN_POSITIVES or text not in texts:
            continue
        mentions = matcher.mentions(label["name"])
        vector = test_vectors @ texts[text]
        r = int(truth.sum())
        sizes = np.array([max(largest.get((s.key, name), 0.0) for name in coco) for s in test_samples])
        entry = {"label": label["name"], "positives": r,
                 "classes": [sorted(matcher.names[i] for i in ids) for _w, ids in mentions],
                 "classIds": sorted(set().union(*[ids for _w, ids in mentions]))}
        rankings = {"vector": np.argsort(-vector, kind="stable")}
        detector = mention_score(test_classes, mentions)
        tiers, absent = {}, {}
        if calibration is not None:
            fused, tier = product(label["name"], mentions, test_classes, vector, calibration)
            rankings["product"] = product_order(fused, tier)
            tiers["product"] = product_first_group(fused, tier)
            entry["productTier"] = tier is not None
            if tier is not None:
                # Without the thing in the library the tier shows only false
                # detections, and when it is empty the page says nothing was found.
                absent["product"] = int(tier[~truth].sum())
            else:
                negatives = fused[~truth]
                absent["product"] = int((negatives >= negatives.max() - GAP).sum())
        if detector is not None:
            rankings["detector"] = rank_order(detector, vector)
            rankings["rrf"] = rrf_rank(vector, detector)
            entry["coverage"] = round(coverage(label["name"], mentions), 2)
            rankings.update(hybrid_rankings(vector, detector, mention_any_score(test_classes, mentions),
                                            coverage(label["name"], mentions)))
            for threshold in THRESHOLDS:
                rankings[f"tiers @{threshold:.1f}"], tiers[f"tiers @{threshold:.1f}"] = tier_rank(vector, detector, threshold)
            cal_samples, _cal_vectors, cal_classes = library[other]
            cal_truth = np.array([label["id"] in s.positive for s in cal_samples])
            calibrated = calibrated_threshold(mention_score(cal_classes, mentions), cal_truth)
            entry["calibratedThreshold"] = calibrated
            if calibrated is not None:
                rankings["tiers @calibrated"], tiers["tiers @calibrated"] = tier_rank(vector, detector, calibrated)
        # The vector rule search uses today to mark the closest group.
        tiers["vector closest"] = vector >= vector.max() - GAP
        for name, order in rankings.items():
            score = np.empty(len(order))
            score[order] = np.arange(len(order), 0, -1)
            entry[name] = {"recallAtR": round(float(recall_at(order, truth, r)), 3),
                           "AP": round(float(average_precision_score(truth, score)), 3)}
            top = np.zeros(len(order), dtype=bool)
            top[order[:r]] = True
            for size, low, high in SIZES:
                in_size = truth & (sizes >= low) & (sizes < high)
                if in_size.sum():
                    entry[name][size] = [int((top & in_size).sum()), int(in_size.sum())]
        for name, shown in tiers.items():
            entry.setdefault(name, {})
            entry[name]["tier"] = {"shown": int(shown.sum()),
                                   "precision": round(float(truth[shown].mean()), 3) if shown.any() else None,
                                   "recall": round(float(shown[truth].mean()), 3)}
            # Without the thing in the library: how many photos would the first tier still show?
            if name in absent:
                entry[name]["tier"]["shownWhenAbsent"] = absent[name]
            elif name == "vector closest":
                entry[name]["tier"]["shownWhenAbsent"] = int((vector[~truth] >= vector[~truth].max() - GAP).sum())
            else:
                entry[name]["tier"]["shownWhenAbsent"] = int(shown[~truth].sum())
        per_query.append(entry)
    return summarize(per_query), per_query


def summarize(per_query):
    """Averages over the queries every ranking applies to, with size buckets pooled."""
    names = sorted({k for q in per_query for k, v in q.items() if isinstance(v, dict)})
    summary = {}
    for name in names:
        found = [q[name] for q in per_query if name in q]
        both = [q for q in per_query if name in q and "detector" in q]
        entry = {"queries": len(found)}
        ranked = [f for f in found if "recallAtR" in f]
        if ranked:
            entry["recallAtR"] = round(float(np.mean([f["recallAtR"] for f in ranked])), 3)
            entry["meanAP"] = round(float(np.mean([f["AP"] for f in ranked])), 3)
            for size, _low, _high in SIZES:
                hit = sum(f[size][0] for f in ranked if size in f)
                total = sum(f[size][1] for f in ranked if size in f)
                if total:
                    entry[size] = round(hit / total, 3)
        tiered = [f["tier"] for f in found if "tier" in f]
        if tiered:
            precise = [t["precision"] for t in tiered if t["precision"] is not None]
            entry["tierPrecision"] = round(float(np.mean(precise)), 3) if precise else None
            entry["tierRecall"] = round(float(np.mean([t["recall"] for t in tiered])), 3)
            entry["tierShownMedian"] = int(np.median([t["shown"] for t in tiered]))
            entry["absentShowsNothing"] = round(float(np.mean([t["shownWhenAbsent"] == 0 for t in tiered])), 3)
            entry["absentShownMedian"] = int(np.median([t["shownWhenAbsent"] for t in tiered]))
        entry["withDetector"] = len(both)
        summary[name] = entry
    return summary


def caption_report(vectors, detections, matcher, kind="written", calibration=None):
    images = load_vectors(vectors, "images")
    texts = load_vectors(vectors, "texts")
    found, captions = datasets.coco_cn("test")
    names = [name for name, _path in found if f"coco-cn:{name}" in images and f"coco-cn:{name}" in detections]
    if not names:
        return {"images": 0}
    keys = [f"coco-cn:{name}" for name in names]
    gallery = np.stack([images[k] for k in keys])
    classes = class_scores(detections, keys)
    # Every query is ranked every way; a query that names no class keeps the vector order.
    queries = []
    for target, name in enumerate(names):
        for caption in captions[name][kind]:
            text = QUERY_PROMPT.format(caption)
            if text in texts:
                queries.append((target, caption, gallery @ texts[text], matcher.mentions(caption)))
    hits = collections.defaultdict(list)
    shares = []
    for target, caption, vector, mentions in queries:
        rankings = {"vector": np.argsort(-vector, kind="stable")}
        if mentions:
            detector = mention_score(classes, mentions)
            shares.append(coverage(caption, mentions))
            rankings["rrf"] = rrf_rank(vector, detector)
            for threshold in THRESHOLDS:
                rankings[f"tiers @{threshold:.1f}"] = tier_rank(vector, detector, threshold)[0]
            rankings.update(hybrid_rankings(vector, detector, mention_any_score(classes, mentions), shares[-1]))
        if calibration is not None:
            rankings["product"] = product_order(*product(caption, mentions, classes, vector, calibration))
            hits["product"].append(int(np.flatnonzero(rankings["product"] == target)[0]))
        hits["vector"].append(int(np.flatnonzero(rankings["vector"] == target)[0]))
        for ranking in RANKINGS_FOR_CAPTIONS:
            order = rankings.get(ranking, rankings["vector"])
            hits[ranking].append(int(np.flatnonzero(order == target)[0]))
    report = {"images": len(names), "queries": len(queries), "queriesNamingAClass": len(shares),
              "coverageMedian": round(float(np.median(shares)), 2) if shares else None}
    for ranking, ranks in hits.items():
        ranks = np.array(ranks)
        report[ranking] = {f"R@{k}": round(float((ranks < k).mean()), 3) for k in (1, 5, 10)}
    return report


def query_texts(path=TABLE):
    with open(path, encoding="utf-8") as file:
        return sorted({QUERY_PROMPT.format(entry["zh"]) for entry in json.load(file)["classes"]})


def encode_queries(embedder_path, directory, weight_cache):
    """Encodes the Chinese class names the vector cache lacks into <detections>/queries.npz."""
    from anas_ai import providers

    cache = Cache(os.path.join(directory, "queries.npz"))
    todo = [text for text in query_texts() if text not in cache.vectors]
    if not todo:
        return
    os.makedirs(weight_cache, exist_ok=True)
    provider = providers.MediaPipeProvider(embedder_path, MANIFEST, vision_tokens=70, cache_dir=weight_cache)
    for text in todo:
        cache.vectors[text] = np.asarray(provider.embed_text(text), dtype=np.float32)
    cache.save()
    provider.close()
    print(f"{len(todo)} query texts encoded", flush=True)


def openimages_classes_for(table=TABLE, root=datasets.ROOT):
    """{Objects365 class id: Open Images class IDs} by English name, plural and alias."""
    names = datasets.openimages_classes(root)
    with open(table, encoding="utf-8") as file:
        classes = json.load(file)["classes"]
    found = {}
    for entry in classes:
        words = [entry["en"]] + [part.strip() for part in entry["en"].split("/")] + OPENIMAGES_ALIASES.get(entry["en"], [])
        words = {w.lower() for w in words} | {w.lower() + "s" for w in words} | {w.lower()[:-1] for w in words if w.endswith("s")}
        ids = set().union(*[names[w] for w in words if w in names]) if any(w in names for w in words) else set()
        if ids:
            found[entry["id"]] = (entry, ids)
    return found


def openimages_report(vectors, detections, query_vectors, matcher, calibration=None, half=None):
    """Ranks Open Images classes on the verified photos, of one half when given."""
    images = load_vectors(vectors, "images")
    texts = {**load_vectors(vectors, "texts"), **query_vectors}
    verified = datasets.openimages_verified()
    keys = sorted(k for k in images if k.startswith("oi:") and k in detections
                  and (half is None or datasets.Sample(k, "").split == half))
    if not keys:
        return {"images": 0}
    gallery = np.stack([images[k] for k in keys])
    classes = class_scores(detections, keys)
    labels = [verified.get(k[3:], {}) for k in keys]
    with open(os.path.join(datasets.ROOT, datasets.COCO_ANNOTATIONS), encoding="utf-8") as file:
        coco_names = {entry["name"] for entry in json.load(file)["categories"]}
    per_class = []
    for cls, (entry, ids) in sorted(openimages_classes_for().items()):
        truth = np.array([1 if any(lab.get(i) is True for i in ids) else 0 if any(lab.get(i) is False for i in ids) else -1
                          for lab in labels])
        known = truth >= 0
        positives = int((truth == 1).sum())
        text = QUERY_PROMPT.format(entry["zh"])
        if positives < MIN_POSITIVES or (truth == 0).sum() < MIN_POSITIVES or text not in texts:
            continue
        mentions = matcher.mentions(entry["zh"]) or [(entry["zh"], frozenset({cls}))]
        vector = gallery[known] @ texts[text]
        detector = mention_score(classes[known], mentions)
        y = truth[known]
        result = {"class": entry["en"], "zh": entry["zh"], "positives": positives, "negatives": int((y == 0).sum()),
                  "classIds": sorted(set().union(*[ids for _w, ids in mentions])),
                  "inCoco": any(part.strip().lower() in coco_names for part in entry["en"].split("/")),
                  "vectorAP": round(float(average_precision_score(y, vector)), 3),
                  "detectorAP": round(float(average_precision_score(y, detector + 1e-6 * vector)), 3)}
        if calibration is not None:
            fused, tier = product(entry["zh"], mentions, classes[known], vector, calibration)
            order = product_order(fused, tier)
            ranked = np.empty(len(order))
            ranked[order] = np.arange(len(order), 0, -1)
            result["productAP"] = round(float(average_precision_score(y, ranked)), 3)
        for threshold in (0.3, 0.5, 0.6):
            shown = detector >= threshold
            result[f"detector@{threshold:.1f}"] = {
                "precision": round(float(y[shown].mean()), 3) if shown.any() else None,
                "recall": round(float(shown[y == 1].mean()), 3)}
        tier = (detector >= 0.5).astype(np.float32)
        order = rank_order(tier, vector)
        score = np.empty(len(order))
        score[order] = np.arange(len(order), 0, -1)
        result["tiersAP"] = round(float(average_precision_score(y, score)), 3)
        # A class name is all class words, so coverage-weighted fusion is vector + weight x detector.
        result["fusedAP"] = round(float(average_precision_score(y, vector + FUSION_WEIGHT * detector)), 3)
        per_class.append(result)
    summary = {"images": len(keys), "classes": len(per_class)}
    for group, chosen in (("all", per_class), ("inCoco", [c for c in per_class if c["inCoco"]]),
                          ("beyondCoco", [c for c in per_class if not c["inCoco"]])):
        if not chosen:
            continue
        summary[group] = {"classes": len(chosen),
                          **{name: round(float(np.mean([c[name] for c in chosen])), 3)
                             for name in ("vectorAP", "detectorAP", "tiersAP", "fusedAP", "productAP")
                             if all(name in c for c in chosen)}}
        for threshold in ("0.3", "0.5", "0.6"):
            precise = [c[f"detector@{threshold}"]["precision"] for c in chosen if c[f"detector@{threshold}"]["precision"] is not None]
            summary[group][f"detector@{threshold}"] = {
                "precision": round(float(np.mean(precise)), 3) if precise else None,
                "recall": round(float(np.mean([c[f"detector@{threshold}"]["recall"] for c in chosen])), 3)}
    return summary, per_class


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--vectors", required=True, help="eval.embed cache directory (whole-photo vectors)")
    parser.add_argument("--detections", required=True, help="eval.detect cache directory")
    parser.add_argument("--embedder", help="EmbeddingGemma .litertlm to encode the Chinese class names")
    parser.add_argument("--weight-cache", default=os.path.join(datasets.ROOT, "cache", "xnnpack"))
    parser.add_argument("--report", help="write the full report as JSON")
    parser.add_argument("--write-calibration", action="store_true",
                        help=f"write the fusion and tier lists to {CALIBRATION}")
    args = parser.parse_args()
    if args.embedder:
        encode_queries(args.embedder, args.detections, args.weight_cache)
    detections = load_detections(os.path.join(args.detections, "detections.npz"))
    queries_path = os.path.join(args.detections, "queries.npz")
    query_vectors = Cache(queries_path).vectors if os.path.exists(queries_path) else {}
    with open(os.path.join(args.detections, "labels.json"), encoding="utf-8") as file:
        model = json.load(file)["model"]
    matcher = Matcher()

    # The lists come from the calibration halves of COCO and Open Images only;
    # everything reported below is measured on the test halves, which choose
    # nothing, and on COCO-CN.
    _summary, calibration_queries = label_report(args.vectors, detections, matcher, "calibration")
    openimages = openimages_report(args.vectors, detections, query_vectors, matcher, half="calibration")
    _oi_summary, oi_calibration = openimages if isinstance(openimages, tuple) else (openimages, [])
    lists, words = derive_lists([calibration_queries], oi_calibration)
    calibration = {"weight": FUSION_WEIGHT, "tierScore": TIER_SCORE, "tierCoverage": TIER_COVERAGE, **lists}

    report = {"model": model, "words": words, "lists": {name: sorted(value) for name, value in lists.items()}}
    report["labels"], report["perQuery"] = label_report(args.vectors, detections, matcher, "test", calibration)
    report["captions"] = caption_report(args.vectors, detections, matcher, calibration=calibration)
    openimages = openimages_report(args.vectors, detections, query_vectors, matcher, calibration, half="test")
    report["openimages"], report["openimagesClasses"] = openimages if isinstance(openimages, tuple) else (openimages, [])
    print(json.dumps({key: report[key] for key in ("lists", "labels", "captions", "openimages")},
                     ensure_ascii=False, indent=1))
    if args.report:
        with open(args.report, "w", encoding="utf-8") as file:
            json.dump(report, file, ensure_ascii=False, indent=1)
    if args.write_calibration:
        write_calibration(model, calibration)


def write_calibration(model, calibration, path=CALIBRATION):
    """The photo service's fusion and tier lists for one detector model."""
    lines = ["{",
             f' "about": {json.dumps(CALIBRATION_ABOUT, ensure_ascii=False)},',
             f' "model": {json.dumps(model)},',
             f' "weight": {calibration["weight"]},',
             f' "tierScore": {calibration["tierScore"]},',
             f' "tierCoverage": {calibration["tierCoverage"]},',
             f' "fusion": {json.dumps(sorted(calibration["fusion"]))},',
             f' "tier": {json.dumps(sorted(calibration["tier"]))},',
             f' "disabledWords": {json.dumps(sorted(calibration["disabledWords"]), ensure_ascii=False)}',
             "}"]
    with open(path, "w", encoding="utf-8", newline="\n") as file:
        file.write("\n".join(lines) + "\n")
    print(f"wrote {path}: {len(calibration['fusion'])} fusion classes, {len(calibration['tier'])} tier classes")


if __name__ == "__main__":
    main()
