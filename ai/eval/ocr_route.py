"""Compares ways to pick the photos whose text the library reads.

Development only (docs/architecture/photo-ocr.md#哪些照片读文字). Reading one
photo costs the NAS about 105 s, so routing must find nearly every document,
receipt and card (kind "read" in eval.text_sets) while passing over ordinary
photos ("photo") and words on signs and packaging ("scene"). Web images with
text ("web") are reported apart.

Methods, each a score where higher means "read it":

- embedding: the photo's EmbeddingGemma vector, which the library already
  keeps, against search queries for documents (READ), taking the highest
  cosine similarity; embedding-contrast subtracts the highest against
  ordinary scenes (PHOTO). No model is added.
- detector features from eval.text_detect: the share of the photo the boxes
  cover (box_share), the share marked as text (text) and the box count.
- either: read when the embedding or the detector clears its own threshold.

Thresholds are set on one stable half of the photos for a target recall of
"read" photos, and everything is reported on the other half, so the numbers
are not tuned to the photos they describe.

    python -m eval.ocr_route --model /models/embeddinggemma-2-740m.litertlm \\
        --embeddings "/datasets/cache/<model ID>-e512" --detections /datasets/cache/text-detect/<run>.json \\
        --report /datasets/runs/ocr-route/report.json
"""

import argparse
import hashlib
import json
import os

import numpy as np

from anas_ai import providers
from eval import embed, text_sets

READ = ["证件", "身份证", "银行卡", "驾驶证", "护照", "票据", "发票", "小票", "收据", "银行回单", "转账记录",
        "截图", "手机截图", "文档", "文件", "合同", "表格", "书页", "手写笔记", "名片", "快递单", "病历",
        "说明书", "试卷", "证书",
        # Family libraries also hold documents in English (receipts abroad,
        # forms); with Chinese queries alone, FUNSD's English forms were
        # found 63% of the time.
        "document", "form", "receipt", "invoice", "ID card", "screenshot"]
PHOTO = ["人物合影", "风景", "美食", "宠物", "街道", "建筑", "商店招牌", "商品包装", "室内", "孩子", "旅行"]
TARGETS = (0.99, 0.98, 0.95, 0.90)


def half(key):
    return "calibration" if hashlib.sha256(key.encode()).digest()[0] % 2 == 0 else "test"


def threshold_for(scores, recall):
    """The highest threshold that keeps at least recall of the scores at or above it."""
    ordered = np.sort(np.asarray(scores))[::-1]
    return float(ordered[max(0, int(np.ceil(recall * len(ordered))) - 1)])


def rates(samples, chosen):
    """Share chosen per set and per kind."""
    by = {}
    for sample, picked in zip(samples, chosen, strict=True):
        for group in (sample.group, f"kind:{sample.kind}"):
            count, hits = by.get(group, (0, 0))
            by[group] = (count + 1, hits + int(picked))
    return {group: {"photos": count, "chosen": round(hits / count, 4)} for group, (count, hits) in sorted(by.items())}


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--model", required=True, help="EmbeddingGemma, for the queries")
    parser.add_argument("--embeddings", required=True, help="eval.embed's image vector cache")
    parser.add_argument("--detections", action="append", default=[], help="eval.text_detect results, one per run")
    parser.add_argument("--report", required=True)
    parser.add_argument("--write", help="write the embedding route at --target for the photo service here")
    parser.add_argument("--target", type=float, default=0.98, help="recall of documents the written route keeps")
    args = parser.parse_args()

    vectors = embed.load_vectors(args.embeddings, "images")
    provider = providers.MediaPipeProvider(args.model, embed.MANIFEST, vision_tokens=70)
    model = provider.model
    read = np.stack([np.asarray(provider.embed_query(text), dtype=np.float32) for text in READ])
    photo = np.stack([np.asarray(provider.embed_query(text), dtype=np.float32) for text in PHOTO])
    provider.close()

    detections = {}
    for path in args.detections:
        with open(path, encoding="utf-8") as file:
            detections[os.path.basename(path)[: -len(".json")]] = json.load(file)

    samples = [sample for sample in text_sets.samples() if sample.key in vectors
               and all(sample.key in found for found in detections.values())]
    missing = len(text_sets.samples()) - len(samples)
    matrix = np.stack([vectors[sample.key] for sample in samples])
    scores = {"embedding": (matrix @ read.T).max(axis=1)}
    scores["embedding-contrast"] = scores["embedding"] - (matrix @ photo.T).max(axis=1)
    for run, found in detections.items():
        for feature in ("box_share", "text", "boxes"):
            scores[f"{run}:{feature}"] = np.array([found[sample.key][feature] for sample in samples], dtype=np.float32)

    calibration = np.array([half(sample.key) == "calibration" for sample in samples])
    is_read = np.array([sample.kind == "read" for sample in samples])
    report = {"photos": len(samples), "left_out": missing, "queries": {"read": READ, "photo": PHOTO}, "methods": {}}
    for method, score in scores.items():
        report["methods"][method] = {}
        for target in TARGETS:
            threshold = threshold_for(score[calibration & is_read], target)
            chosen = score >= threshold
            report["methods"][method][str(target)] = {
                "threshold": threshold,
                "test": rates([s for s, c in zip(samples, calibration, strict=True) if not c], chosen[~calibration]),
            }
    # Either the embedding or a detector feature: each threshold set on the
    # calibration half for the same target, then the union reported.
    for run in detections:
        for feature in ("box_share",):
            method = f"either:embedding+{run}:{feature}"
            report["methods"][method] = {}
            for target in TARGETS:
                # Splitting the misses evenly keeps the union's recall near the target.
                each = 1 - (1 - target) / 2
                first = threshold_for(scores["embedding"][calibration & is_read], each)
                second = threshold_for(scores[f"{run}:{feature}"][calibration & is_read], each)
                chosen = (scores["embedding"] >= first) | (scores[f"{run}:{feature}"] >= second)
                report["methods"][method][str(target)] = {
                    "threshold": [first, second],
                    "test": rates([s for s, c in zip(samples, calibration, strict=True) if not c], chosen[~calibration]),
                }
    os.makedirs(os.path.dirname(os.path.abspath(args.report)), exist_ok=True)
    with open(args.report, "w", encoding="utf-8") as file:
        json.dump(report, file, ensure_ascii=False, indent=1)
    if args.write:
        result = report["methods"]["embedding"].get(str(args.target))
        if result is None:
            threshold = threshold_for(scores["embedding"][calibration & is_read], args.target)
            chosen = scores["embedding"] >= threshold
            result = {"threshold": threshold,
                      "test": rates([s for s, c in zip(samples, calibration, strict=True) if not c], chosen[~calibration])}
        route = {
            "model": model,
            "queries": READ,
            "threshold": round(result["threshold"], 4),
            "target": args.target,
            "measured": {kind: result["test"][f"kind:{kind}"] for kind in ("read", "photo", "scene", "web")},
        }
        with open(args.write, "w", encoding="utf-8", newline="\n") as file:
            file.write(json.dumps(route, ensure_ascii=False, indent=2) + "\n")

    print(f"{len(samples)} photos ({missing} without every score); test half; chosen share per kind")
    print(f"{'method':48s}{'target':>7s}{'read':>7s}{'photo':>7s}{'scene':>7s}{'web':>7s}")
    for method, by_target in report["methods"].items():
        for target, result in by_target.items():
            test = result["test"]
            row = "".join(f"{test.get(f'kind:{kind}', {}).get('chosen', 0):7.3f}" for kind in ("read", "photo", "scene", "web"))
            print(f"{method:48s}{target:>7s}{row}")


if __name__ == "__main__":
    main()
