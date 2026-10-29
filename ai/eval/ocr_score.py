"""Scores the answers cached by eval.ocr.

Development only (docs/research/photo-ocr-model-evaluation.md). Fields use
CC-OCR's own KIE evaluator (field-level F1 after its normalisation) and plain
text uses its OCR evaluator (character F1), both loaded from the copy fetched
with the dataset. On top of that it reports what matters for documents in a
photo library:

- text: 1 - normalised edit distance after reducing Markdown, HTML and LaTeX
  to plain text, so that a model's output format does not count as errors;
- fields with digits (amounts, dates, numbers): exact, missing, wrong with the
  same shape (same length, digits in the same places: the error a reader would
  not notice) or otherwise wrong;
- whether the model's own token probabilities flag the wrong fields.

    python -m eval.ocr_score            # all models and subsets found in /runs
    python -m eval.ocr_score --models glm-ocr --subsets cold_sibr
"""

import argparse
import importlib.util
import json
import math
import os
import re
import statistics
import sys
import types

from rapidfuzz.distance import Levenshtein

from eval.ocr import DATA_DIR, RUNS_DIR, SUBSETS, load_subset

THRESHOLDS = (0.5, 0.8, 0.9, 0.99)


def cc_ocr():
    """CC-OCR's evaluator modules, without its package __init__ (which needs the table metrics' extras)."""
    base = os.path.join(DATA_DIR, "evaluation", "evaluator")
    package = types.ModuleType("ccocr_evaluator")
    package.__path__ = [base]
    sys.modules["ccocr_evaluator"] = package
    modules = {}
    for name in ("common", "kie_evaluator", "ocr_evaluator"):
        spec = importlib.util.spec_from_file_location(f"ccocr_evaluator.{name}", os.path.join(base, name + ".py"))
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        modules[name] = module
    return modules


def load_answers(model, subset):
    """The answer per item; an item that only ever failed (e.g. crashed llama-server) counts with an empty output."""
    path = os.path.join(RUNS_DIR, model, subset + ".jsonl")
    answers = {}
    if os.path.exists(path):
        with open(path, encoding="utf-8") as file:
            for line in file:
                record = json.loads(line)
                if "error" not in record or "error" in answers.get(record["index"], {"error": ""}):
                    answers[record["index"]] = record
    for record in answers.values():
        if "error" in record:
            record.update(output="", finish="error", tokens=[], timings=None)
    return answers


def failed(answers):
    return sum("error" in record for record in answers.values())


MARKUP = [
    (re.compile(r"<[^>]+>"), " "),                       # HTML tags (tables)
    (re.compile(r"\\(?:begin|end)\{[^}]*\}"), " "),       # LaTeX environments
    (re.compile(r"\\[a-zA-Z]+\*?"), " "),                 # LaTeX commands
    (re.compile(r"[#*`|$_{}~>\\-]"), " "),                # Markdown and LaTeX punctuation
    (re.compile(r"\s+"), ""),
]


def plain(text, halfwidth):
    text = halfwidth(text)
    for pattern, replacement in MARKUP:
        text = pattern.sub(replacement, text)
    return text.lower()


def text_scores(model, subset, ocr_module, halfwidth):
    items = {item["index"]: item for item in load_subset(subset)}
    answers = load_answers(model, subset)
    if not answers:
        return None
    gts = {index: items[index]["answer"] for index in answers}
    preds = {index: record["output"] for index, record in answers.items()}
    official = ocr_module.OcrEvaluator("multi_scene_ocr").evaluate(preds, gts, dataset=subset)["summary"]
    similarity = []
    for index in answers:
        pred, gt = plain(preds[index], halfwidth), plain(gts[index], halfwidth)
        similarity.append(1 - Levenshtein.normalized_distance(pred, gt) if (pred or gt) else 1.0)
    return {
        "n": len(answers),
        "char_f1": official["macro_f1_score"],
        "text_similarity": statistics.mean(similarity),
        "runaway": sum(record["finish"] in ("length", "repetition") for record in answers.values()),
        "failed": failed(answers),
    }


DIGITS = re.compile(r"\d")


def is_numberlike(value):
    digits = len(DIGITS.findall(value))
    return digits >= 4 and digits / max(1, len(value.replace(" ", ""))) >= 0.5


def shape(value):
    return re.sub(r"\d", "9", re.sub(r"[^\d\s]", "x", value))


def field_confidence(record, raw_value):
    """Lowest token probability over the tokens that spell raw_value in the output, or None."""
    tokens = record.get("tokens") or []
    text = "".join(token for token, _ in tokens)
    start = text.find(raw_value)
    if start < 0 or not raw_value:
        return None
    end, position, lowest = start + len(raw_value), 0, 0.0
    for token, logprob in tokens:
        token_end = position + len(token)
        if token_end > start and position < end:
            lowest = min(lowest, logprob)
        position = token_end
        if position >= end:
            break
    return math.exp(lowest)


EMPTY = {"none", "null", "n/a", "无", "-", "空"}


def top_level(gt, pred):
    """The fields that are plain values in the ground truth, as both sides gave them.

    Cards, receipts and bank slips carry their key facts (name, ID, amount, date,
    account, serial number) at the top level. A model told only the field names
    cannot know the nesting of line-item tables, so nested fields are left out
    here, and spellings of an empty value count as empty.
    """
    keys = [key for key, value in gt.items() if isinstance(value, (str, int, float))]
    plain = {}
    for key in keys:
        value = pred.get(key, "")
        if isinstance(value, (str, int, float)):
            plain[key] = "" if str(value).strip().lower() in EMPTY else value
        else:
            plain[key] = json.dumps(value, ensure_ascii=False)
    return {key: gt[key] for key in keys}, plain


def number_fields(kie, normalize, gts, preds, answers):
    """Number-like ground-truth fields: exact, missing, wrong in the same shape or otherwise wrong;
    and for the answered ones, the lowest token probability next to whether they were wrong."""
    counts = {"exact": 0, "missing": 0, "same_shape": 0, "other": 0}
    flagged = []
    for index, gt in gts.items():
        gt_fields = kie.flatten(kie.normalize_dict(kie.normalize_values_of_nested_dict(gt, normalize)))
        pred_by_key = {}
        for key, value in kie.flatten(kie.normalize_dict(preds.get(index) or {})):
            if normalize(value).strip():  # an empty answer is a missing field, not a wrong one
                pred_by_key.setdefault(key, []).append((normalize(value), value))
        for key, value in gt_fields:
            if not is_numberlike(value):
                continue
            candidates = pred_by_key.get(key, [])
            match = next((c for c in candidates if c[0] == value), None)
            if match:
                counts["exact"] += 1
                candidates.remove(match)
                wrong, raw = False, match[1]
            elif not candidates:
                counts["missing"] += 1
                continue
            else:
                near = next((c for c in candidates if shape(c[0]) == shape(value)), None)
                counts["same_shape" if near else "other"] += 1
                chosen = near or candidates[0]
                candidates.remove(chosen)
                wrong, raw = True, chosen[1]
            confidence = field_confidence(answers[index], raw)
            if confidence is not None:
                flagged.append((confidence, wrong))
    answered = counts["exact"] + counts["same_shape"] + counts["other"]
    result = {"number_fields": counts, "number_exact_of_answered": counts["exact"] / answered if answered else None}
    wrong_total = sum(1 for _, wrong in flagged if wrong)
    for threshold in THRESHOLDS:
        low = [wrong for confidence, wrong in flagged if confidence < threshold]
        result[f"flag<{threshold}"] = {
            "flagged": len(low),
            "catches_wrong": (sum(low) / wrong_total) if wrong_total else None,
            "wrong_among_flagged": (sum(low) / len(low)) if low else None,
        }
    return result


def kie_scores(model, subset, modules):
    kie = modules["kie_evaluator"]
    items = {item["index"]: item for item in load_subset(subset)}
    answers = load_answers(model, subset)
    if not answers:
        return None
    evaluator = kie.KieEvaluator("kie")
    normalize = evaluator.normalize_func
    gts = {index: json.loads(items[index]["answer"]) for index in answers}
    raw_preds = {index: kie.post_process_to_json(record["output"]) for index, record in answers.items()}
    parsed = {index: pred for index, pred in raw_preds.items() if isinstance(pred, dict)}
    official = evaluator.evaluate(dict(parsed), dict(gts), dataset=subset)["summary"]
    result = {
        "n": len(answers),
        "json_ok": len(parsed) / len(answers),
        "runaway": sum(record["finish"] in ("length", "repetition") for record in answers.values()),
        "failed": failed(answers),
        "field_f1": official["f1_score"],
        "nted_acc": official["acc"],
        **number_fields(kie, normalize, gts, parsed, answers),
    }
    pairs = {index: top_level(gt, parsed.get(index) or {}) for index, gt in gts.items()}
    top_gts = {index: pair[0] for index, pair in pairs.items()}
    top_preds = {index: pair[1] for index, pair in pairs.items()}
    top = evaluator.evaluate(dict(top_preds), dict(top_gts), dataset=subset)["summary"]
    result["top_level"] = {"field_f1": top["f1_score"], **number_fields(kie, normalize, top_gts, top_preds, answers)}
    return result


def speed(model, subset):
    answers = {index: record for index, record in load_answers(model, subset).items() if "error" not in record}
    if not answers:
        return None
    seconds = sorted(record["seconds"] for record in answers.values())
    first = [record["first_token_seconds"] for record in answers.values() if record.get("first_token_seconds")]
    timings = [record["timings"] for record in answers.values() if record.get("timings")]
    return {
        "median_s": statistics.median(seconds),
        "p90_s": seconds[int(0.9 * (len(seconds) - 1))],
        # image encoding plus prompt, before the first output token
        "first_token_s": statistics.median(first) if first else None,
        "prompt_tokens": statistics.median(t["prompt_n"] for t in timings) if timings else None,
        "output_tokens": statistics.median(t["predicted_n"] for t in timings) if timings else None,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--models", default="")
    parser.add_argument("--subsets", default=",".join(SUBSETS))
    args = parser.parse_args()
    models = [m for m in args.models.split(",") if m] or sorted(
        d for d in os.listdir(RUNS_DIR) if os.path.isdir(os.path.join(RUNS_DIR, d)) and not d.startswith("bench"))
    modules = cc_ocr()
    halfwidth = modules["kie_evaluator"].fullwidth_to_halfwidth
    report = {}
    for model in models:
        for subset in [s for s in args.subsets.split(",") if s]:
            kind = SUBSETS[subset][1]
            scores = kie_scores(model, subset, modules) if kind == "kie" else text_scores(
                model, subset, modules["ocr_evaluator"], halfwidth)
            if scores is None:
                continue
            scores["speed"] = speed(model, subset)
            report.setdefault(model, {})[subset] = scores
            print(model, subset, json.dumps(scores, ensure_ascii=False), flush=True)
    with open(os.path.join(RUNS_DIR, "report.json"), "w", encoding="utf-8") as file:
        json.dump(report, file, ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
