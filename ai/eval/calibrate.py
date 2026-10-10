"""Calibrates the AI label thresholds and measures label and search quality.

Reads the vectors cached by eval.embed. Each label's threshold is the lowest
score whose false-positive rate on the calibration half's COCO negatives
stays within a limit; a label without enough known negatives, positives or
recall stays search-only. The limit is the loosest one in a sweep at which
the labels shown on the calibration half's COCO photos are still right at
least --min-precision of the time; --max-fpr fixes it instead. COCO negatives follow how photos occur; Open
Images negatives were picked because a model mistook them, so they are
reported separately as hard negatives instead of setting thresholds. Recall
counts positives from both. The test half is never used to choose anything.

    python -m eval.calibrate --cache /datasets/cache/<model>-e512 [--write]

--compare-with measures another setting (vision tokens, input edge) on the
images both caches hold, using the calibrated template.
"""

import argparse
import json
import math
import os

import numpy as np
from sklearn.metrics import average_precision_score

from eval import datasets
from eval.embed import CAPTION_TEMPLATES, LABEL_PHRASES, LABEL_TEMPLATES, QUERY_PROMPT, load_vectors

MIN_POSITIVES = 20  # in the calibration half
MIN_RECALL = 0.2  # a label that rarely fires is not worth showing
SHOWN = 5  # most labels shown per photo
REFERENCE_PREVALENCE = 0.05  # precision is also quoted as if 5% of photos held the label
CALIBRATION = os.path.join(datasets.HERE, "labels", "v1.calibration.json")


def label_matrix(vocabulary, texts, template, synonyms):
    rows = []
    for label in vocabulary:
        words = [label["name"]] + (label["synonyms"] if synonyms else [])
        mean = np.mean([texts[template.format(word)] for word in words], axis=0)
        rows.append(mean / np.linalg.norm(mean))
    return np.stack(rows)


def truth_matrix(samples, ids):
    index = {label: i for i, label in enumerate(ids)}
    truth = np.full((len(samples), len(ids)), -1, dtype=np.int8)
    for row, sample in enumerate(samples):
        for label in sample.positive:
            truth[row, index[label]] = 1
        for label in sample.negative:
            truth[row, index[label]] = 0
    return truth


def mean_ap(scores, truth, rows):
    values = []
    for column in range(scores.shape[1]):
        known = rows & (truth[:, column] >= 0)
        labels = truth[known, column]
        if labels.sum() >= MIN_POSITIVES and (labels == 0).sum() >= MIN_POSITIVES:
            values.append(average_precision_score(labels, scores[known, column]))
    return (float(np.mean(values)) if values else float("nan")), len(values)


def thresholds_for(scores, truth, rows, natural, max_fpr, subclasses=frozenset()):
    """Returns {column: threshold} and {column: reason} for the skipped ones.

    Positives come from rows; negatives only from rows that are also natural.
    Subclass labels, whose COCO negatives leave out every photo of their
    parent class, never had the photos they are most easily mistaken for
    (other birds for 鹦鹉) among their negatives, so they stay search-only.
    """
    chosen, skipped = {}, {}
    min_negatives = math.ceil(2 / max_fpr)  # enough to see two false positives at the limit
    for column in range(scores.shape[1]):
        if column in subclasses:
            skipped[column] = "subclass without sibling negatives"
            continue
        positive = scores[rows & (truth[:, column] == 1), column]
        negative = np.sort(scores[rows & natural & (truth[:, column] == 0), column])[::-1]
        if len(positive) < MIN_POSITIVES:
            skipped[column] = f"{len(positive)} positives"
            continue
        if len(negative) < min_negatives:
            skipped[column] = f"{len(negative)} negatives"
            continue
        allowed = math.floor(max_fpr * len(negative))
        threshold = float(negative[allowed]) + 1e-6
        recall = float(np.mean(positive >= threshold))
        if recall < MIN_RECALL:
            skipped[column] = f"recall {recall:.2f}"
            continue
        chosen[column] = threshold
    return chosen, skipped


def shown(scores, thresholds):
    """The labels a photo would show: above threshold, by margin, at most SHOWN."""
    if not thresholds:
        return [[] for _row in scores]
    columns = np.array(sorted(thresholds))
    limits = np.array([thresholds[column] for column in columns])
    margin = scores[:, columns] - limits
    result = []
    for row in margin:
        order = np.argsort(-row)[:SHOWN]
        result.append([int(columns[i]) for i in order if row[i] >= 0])
    return result


def display_quality(scores, truth, rows, thresholds):
    correct = wrong = unknown = photos = 0
    for row, columns in zip(np.flatnonzero(rows), shown(scores[rows], thresholds)):
        photos += bool(columns)
        for column in columns:
            value = int(truth[row, column])
            correct += value == 1
            wrong += value == 0
            unknown += value == -1
    judged = correct + wrong
    return {
        "photos": int(rows.sum()),
        "photosWithLabels": photos,
        "shown": correct + wrong + unknown,
        "precision": correct / judged if judged else float("nan"),
        "judged": judged,
    }


def label_quality(scores, truth, rows, natural, thresholds, ids):
    report = {}
    for column, threshold in thresholds.items():
        positive = scores[rows & (truth[:, column] == 1), column]
        negative = scores[rows & natural & (truth[:, column] == 0), column]
        hard = scores[rows & ~natural & (truth[:, column] == 0), column]
        recall = float(np.mean(positive >= threshold)) if len(positive) else float("nan")
        fpr = float(np.mean(negative >= threshold)) if len(negative) else float("nan")
        reference = REFERENCE_PREVALENCE * recall
        report[ids[column]] = {
            "threshold": round(threshold, 4),
            "positives": len(positive),
            "negatives": len(negative),
            "recall": round(recall, 3),
            "falsePositiveRate": round(fpr, 4),
            "precisionAt5Percent": round(reference / (reference + (1 - REFERENCE_PREVALENCE) * fpr), 3)
            if reference + fpr > 0 else None,
            "hardNegatives": len(hard),
            "hardFalsePositiveRate": round(float(np.mean(hard >= threshold)), 3) if len(hard) else None,
        }
    return report


def labelled(images, vocabulary, ground_truth):
    """The evaluation images present in images: samples, vectors, truth."""
    samples = datasets.coco_samples(ground_truth) + datasets.openimages_samples(
        ground_truth, datasets.select_openimages(ground_truth))
    samples = [sample for sample in samples if sample.key in images]
    vectors = np.stack([images[sample.key] for sample in samples])
    return samples, vectors, truth_matrix(samples, [label["id"] for label in vocabulary])


def compare(cache, other_cache, calibration_path):
    """Ranks labels and captions on the images both caches hold."""
    with open(calibration_path, encoding="utf-8") as file:
        calibrated = json.load(file)
    vocabulary, ground_truth = datasets.load_vocabulary(), datasets.load_ground_truth()
    baseline, other = load_vectors(cache, "images"), load_vectors(other_cache, "images")
    shared = sorted(set(baseline) & set(other))
    cosine = np.array([float(baseline[key] @ other[key]) for key in shared])
    print(f"{len(shared)} shared images: cosine mean {cosine.mean():.4f}, "
          f"5th percentile {np.percentile(cosine, 5):.4f}, min {cosine.min():.4f}")
    # Text vectors do not depend on the image settings; the baseline's serve both.
    texts = load_vectors(cache, "texts")
    for name, images in (("baseline", baseline), ("other", other)):
        subset = {key: images[key] for key in shared}
        result = {}
        if any(key.startswith(("coco:", "oi:")) for key in subset):
            _samples, vectors, truth = labelled(subset, vocabulary, ground_truth)
            template = QUERY_PROMPT.format(calibrated["phrase"])
            scores = vectors @ label_matrix(vocabulary, texts, template, calibrated["synonyms"]).T
            result["meanAP"], result["labels"] = mean_ap(scores, truth, np.ones(len(vectors), dtype=bool))
        if any(key.startswith("coco-cn:") for key in subset):
            result["search"] = retrieval(subset, texts, "test", "written", CAPTION_TEMPLATES["query"])
        print(f"{name:8s} {result}")


def retrieval(images, texts, split, kind, template):
    """Text-to-image recall@1/5/10 on a COCO-CN split."""
    found, captions = datasets.coco_cn(split)
    names = [name for name, _path in found if f"coco-cn:{name}" in images]
    gallery = np.stack([images[f"coco-cn:{name}"] for name in names])
    queries, targets = [], []
    for target, name in enumerate(names):
        for caption in captions[name][kind]:
            queries.append(texts[template.format(caption)])
            targets.append(target)
    ranks = np.argsort(-(np.stack(queries) @ gallery.T), axis=1)
    hits = ranks == np.array(targets)[:, None]
    return {"images": len(names), "queries": len(queries),
            **{f"R@{k}": round(float(hits[:, :k].any(axis=1).mean()), 3) for k in (1, 5, 10)}}


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--cache", required=True, help="directory eval.embed wrote")
    parser.add_argument("--min-precision", type=float, default=0.9,
                        help="share of shown labels that must be right on the calibration half's COCO photos")
    parser.add_argument("--max-fpr", type=float, help="fix the per-label false-positive limit instead")
    parser.add_argument("--write", nargs="?", const=CALIBRATION, metavar="PATH",
                        help=f"write the thresholds (default {os.path.relpath(CALIBRATION, datasets.REPO)})")
    parser.add_argument("--calibration", default=CALIBRATION, help="thresholds whose template --compare-with uses")
    parser.add_argument("--report", help="write the full report as JSON to this file")
    parser.add_argument("--compare-with", help="another cache to measure against this one")
    args = parser.parse_args()
    if args.compare_with:
        compare(args.cache, args.compare_with, args.calibration)
        return

    images = load_vectors(args.cache, "images")
    texts = load_vectors(args.cache, "texts")
    model = os.path.basename(os.path.normpath(args.cache))
    vocabulary = datasets.load_vocabulary()
    ids = [label["id"] for label in vocabulary]
    ground_truth = datasets.load_ground_truth()
    samples, vectors, truth = labelled(images, vocabulary, ground_truth)
    calibration = np.array([sample.split == "calibration" for sample in samples])
    subclasses = frozenset(i for i, label in enumerate(ids) if ground_truth.get(label, {}).get("cocoAbsentUnless"))
    test = ~calibration
    coco = np.array([sample.key.startswith("coco:") for sample in samples])
    report = {"model": model, "images": {"coco": int(coco.sum()), "openimages": int((~coco).sum())}}
    print(f"{model}: {coco.sum()} COCO and {(~coco).sum()} Open Images images")

    # Prompt: mean AP on the calibration half picks the template.
    report["templates"] = {}
    best = None
    for name, template in LABEL_TEMPLATES.items():
        for synonyms in (False, True):
            scores = vectors @ label_matrix(vocabulary, texts, template, synonyms).T
            value, count = mean_ap(scores, truth, calibration)
            key = f"{name}{'+synonyms' if synonyms else ''}"
            report["templates"][key] = {"meanAP": round(value, 4), "labels": count,
                                        "cocoMeanAP": round(mean_ap(scores, truth, calibration & coco)[0], 4)}
            print(f"template {key:16s} mean AP {value:.4f} over {count} labels")
            # Only phrases the Worker can reproduce are eligible.
            if name in LABEL_PHRASES and (best is None or value > best[0]):
                best = (value, name, template, synonyms)
    _value, template_name, template, synonyms = best
    scores = vectors @ label_matrix(vocabulary, texts, template, synonyms).T
    report["template"] = {"name": template_name, "phrase": LABEL_PHRASES[template_name], "synonyms": synonyms}

    # The false-positive limit: the loosest that keeps shown labels right.
    report["sweep"] = {}
    max_fpr_chosen = args.max_fpr
    for max_fpr in (0.0005, 0.001, 0.0015, 0.002, 0.003, 0.005, 0.01):
        chosen, _skipped = thresholds_for(scores, truth, calibration, coco, max_fpr, subclasses)
        quality = display_quality(scores, truth, calibration & coco, chosen)
        report["sweep"][str(max_fpr)] = {"labels": len(chosen), "cocoCalibration": quality}
        if args.max_fpr is None and quality["judged"] and quality["precision"] >= args.min_precision:
            max_fpr_chosen = max_fpr
        print(f"max FPR {max_fpr:<6} {len(chosen):3d} labels; COCO calibration: {quality['precision']:.3f} precision "
              f"of {quality['judged']} judged, {quality['photosWithLabels']}/{quality['photos']} photos labelled")

    if max_fpr_chosen is None:
        raise SystemExit(f"no false-positive limit keeps {args.min_precision:.0%} of shown labels right")
    chosen, skipped = thresholds_for(scores, truth, calibration, coco, max_fpr_chosen, subclasses)
    report["maxFalsePositiveRate"] = max_fpr_chosen
    print(f"chosen max FPR {max_fpr_chosen}")
    report["test"] = {
        "coco": display_quality(scores, truth, test & coco, chosen),
        "openimages": display_quality(scores, truth, test & ~coco, chosen),
    }
    report["labels"] = label_quality(scores, truth, test, coco, chosen, ids)
    report["searchOnly"] = {ids[column]: reason for column, reason in skipped.items()}
    report["searchOnly"].update({label: "no ground truth" for label in ids if label not in ground_truth})
    for name, quality in report["test"].items():
        print(f"test {name}: {quality['precision']:.3f} precision of {quality['judged']} judged labels, "
              f"{quality['photosWithLabels']}/{quality['photos']} photos labelled")
    print(f"{len(chosen)} labels shown, {len(report['searchOnly'])} search-only")

    report["search"] = {}
    if any(key.startswith("coco-cn:") for key in images):
        for kind in ("written", "translated"):
            for name, caption_template in CAPTION_TEMPLATES.items():
                result = retrieval(images, texts, "test", kind, caption_template)
                report["search"][f"{kind}/{name}"] = result
                print(f"COCO-CN test {kind:10s} {name:6s} {result}")

    if args.report:
        with open(args.report, "w", encoding="utf-8") as file:
            json.dump(report, file, ensure_ascii=False, indent=2)
    if args.write:
        calibrated = {
            "labels": 1,
            "model": model.rsplit("-e", 1)[0],
            "phrase": LABEL_PHRASES[template_name],
            "synonyms": synonyms,
            "maxFalsePositiveRate": max_fpr_chosen,
            "thresholds": {ids[column]: round(value, 4) for column, value in sorted(chosen.items())},
        }
        with open(args.write, "w", encoding="utf-8", newline="\n") as file:
            json.dump(calibrated, file, ensure_ascii=False, indent=2)
            file.write("\n")


if __name__ == "__main__":
    main()
