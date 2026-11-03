"""Measures which checks let a field read from a photo pass without a person looking.

Development only (docs/architecture/photo-ocr.md#核对层). Every field the text
reader fills is either passed or left for a person to check; the measure is
the share of passed fields that are wrong, next to the share passed at all.
This reads CC-OCR's receipts and cards the way the product does, through the
AI Worker's LlamaReader with its prompts and settings: once for the whole
text, once for the fields of the item's schema. It then scores the top-level
fields that have a value in the ground truth (CC-OCR's own normalisation)
under each check:

- text: the value appears in the whole text read separately (ignoring
  spaces, width and case), two independent readings agreeing;
- format: the value is well formed where its key says what it is: a date
  that exists, an amount, an ID number with its GB 11643 check digit, a card
  number with its Luhn digit; fields without a known format pass;
- confidence: every token that spells the value had at least the given
  probability.

    python -m eval.ocr_verify run --server /llama/llama-server --model M.gguf --model-manifest M.json \\
        --mmproj P.gguf --mmproj-manifest P.json --runtime-manifest /src/deploy/llama/llama.json --threads 12
    python -m eval.ocr_verify score --run /runs/verify-<model>

Reading needs only the Worker's runtime (the NAS's has Pillow); scoring
needs eval.ocr_score's dependencies and CC-OCR's evaluator.
"""

import argparse
import base64
import datetime
import json
import os
import re
import time
import unicodedata

from anas_ai import providers
from eval import ocr

SUBSETS = ("cold_sibr", "cold_cell")
PER_SUBSET = 25
CONFIDENCES = (0.9, 0.99, 0.999)
DATE = re.compile(r"^(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})")
AMOUNT = re.compile(r"^[-+]?[¥￥$]?\d{1,3}(,?\d{3})*(\.\d{1,2})?元?$")


def squeeze(text):
    """Text compared regardless of spaces, full- or half-width forms and case."""
    return re.sub(r"\s+", "", unicodedata.normalize("NFKC", str(text))).lower()


def id_number_ok(value):
    """GB 11643: 17 digits, then a check digit, X for 10."""
    value = value.upper()
    if not re.fullmatch(r"\d{17}[\dX]", value):
        return False
    weights = [7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2]
    total = sum(int(digit) * weight for digit, weight in zip(value[:17], weights, strict=True))
    return "10X98765432"[total % 11] == value[17]


def luhn_ok(value):
    digits = [int(d) for d in re.sub(r"\s", "", value) if d.isdigit()]
    if not 12 <= len(digits) <= 19:
        return False
    total = 0
    for position, digit in enumerate(reversed(digits)):
        if position % 2:
            digit *= 2
            digit -= 9 if digit > 9 else 0
        total += digit
    return total % 10 == 0


def format_ok(key, value):
    """Whether value is well formed for what its key names; None when the key names no known format."""
    value = squeeze(value)
    if "身份证" in key or "公民身份号码" in key:
        return id_number_ok(value)
    if "卡号" in key:
        return luhn_ok(value)
    if any(word in key for word in ("日期", "时间", "date")):
        match = DATE.match(value)
        if not match:
            return None  # a layout this check does not know
        try:
            datetime.date(int(match.group(1)), int(match.group(2)), int(match.group(3)))
        except ValueError:
            return False
        return True
    if any(word in key for word in ("金额", "合计", "总计", "总价", "实收", "税额", "price", "total", "amount")):
        return bool(AMOUNT.match(value))
    return None


def run(args):
    reader = providers.LlamaReader([args.server], args.model, args.model_manifest, args.mmproj, args.mmproj_manifest,
                                   args.runtime_manifest, args.threads)
    out = os.path.join(ocr.RUNS_DIR, "verify-" + reader.model)
    os.makedirs(out, exist_ok=True)
    try:
        for subset in args.subsets.split(","):
            path = os.path.join(out, subset + ".jsonl")
            done = set()
            if os.path.exists(path):
                with open(path, encoding="utf-8") as file:
                    done = {json.loads(line)["index"] for line in file if line.strip()}
            for item in ocr.load_subset(subset)[: args.per_subset]:
                if item["index"] in done:
                    continue
                url, _size = ocr.image_url(item["image"])
                data = base64.b64decode(url.split(",", 1)[1])
                record = {"index": item["index"]}
                for kind, schema in (("text", None), ("fields", ocr.schema_of(item["question"]))):
                    started = time.monotonic()
                    reading = reader.read(data, kind, schema, reader.run_options())
                    record[kind] = reading["text"]
                    record[kind + "_tokens"] = reading["logprobs"]
                    record[kind + "_finish"] = reading["finish"]
                    record[kind + "_seconds"] = round(time.monotonic() - started, 1)
                with open(path, "a", encoding="utf-8") as file:
                    file.write(json.dumps(record, ensure_ascii=False) + "\n")
                print(f"{subset} {item['index']}: {record['text_seconds']} s + {record['fields_seconds']} s", flush=True)
    finally:
        reader.release()


def fields(run_dir, subset, normalize, kie):
    """Each answered top-level field: (key, number-like, correct, in text, format, confidence)."""
    from eval import ocr_score  # rapidfuzz, which only scoring needs

    items = {item["index"]: item for item in ocr.load_subset(subset)}
    found = []
    with open(os.path.join(run_dir, subset + ".jsonl"), encoding="utf-8") as file:
        for line in file:
            record = json.loads(line)
            gt = json.loads(items[record["index"]]["answer"])
            pred = kie.post_process_to_json(record["fields"])
            pred = pred if isinstance(pred, dict) else {}
            gt_top, pred_top = ocr_score.top_level(gt, pred)
            text = squeeze(record["text"])
            tokens = {"tokens": record["fields_tokens"]}
            for key, value in gt_top.items():
                expected, answered = normalize(str(value)).strip(), str(pred_top.get(key, "")).strip()
                if not expected or not answered:
                    continue
                found.append({
                    "key": key,
                    "number": ocr_score.is_numberlike(expected),
                    "correct": normalize(answered).strip() == expected,
                    "text": squeeze(answered) in text,
                    "format": format_ok(key, answered),
                    "confidence": ocr_score.field_confidence(tokens, answered),
                })
    return found


def rules():
    yield "every answered field", lambda f: True
    yield "in the whole text", lambda f: f["text"]
    yield "in the text, well formed", lambda f: f["text"] and f["format"] is not False
    for threshold in CONFIDENCES:
        yield f"confidence >= {threshold}", lambda f, t=threshold: f["confidence"] is not None and f["confidence"] >= t
        yield (f"in the text, well formed, confidence >= {threshold}",
               lambda f, t=threshold: f["text"] and f["format"] is not False
               and f["confidence"] is not None and f["confidence"] >= t)


def score(args):
    from eval import ocr_score  # rapidfuzz, which only scoring needs

    modules = ocr_score.cc_ocr()
    kie = modules["kie_evaluator"]
    normalize = kie.KieEvaluator("kie").normalize_func
    report = {}
    for subset in args.subsets.split(","):
        found = fields(args.run, subset, normalize, kie)
        report[subset] = {"fields": len(found), "numbers": sum(f["number"] for f in found), "rules": {}}
        for name, rule in rules():
            for scope, chosen in (("all", found), ("numbers", [f for f in found if f["number"]])):
                passed = [f for f in chosen if rule(f)]
                wrong = sum(not f["correct"] for f in passed)
                report[subset]["rules"].setdefault(name, {})[scope] = {
                    "passed": round(len(passed) / len(chosen), 3) if chosen else None,
                    "wrong_among_passed": round(wrong / len(passed), 4) if passed else None,
                    "wrong_total": sum(not f["correct"] for f in chosen),
                    "wrong_passed": wrong,
                }
    with open(os.path.join(args.run, "verify.json"), "w", encoding="utf-8") as file:
        json.dump(report, file, ensure_ascii=False, indent=1)
    for subset, result in report.items():
        print(f"== {subset}: {result['fields']} answered fields, {result['numbers']} with numbers")
        print(f"   {'rule':52s}{'passed':>8s}{'wrong':>8s}   numbers: {'passed':>7s}{'wrong':>8s}")
        for name, scopes in result["rules"].items():
            row = "".join(f"{scopes[s]['passed'] or 0:8.3f}{scopes[s]['wrong_among_passed'] or 0:8.4f}   "
                          for s in ("all", "numbers"))
            print(f"   {name:52s}{row}")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    reading = sub.add_parser("run")
    for name in ("server", "model", "model-manifest", "mmproj", "mmproj-manifest", "runtime-manifest"):
        reading.add_argument("--" + name, required=True)
    reading.add_argument("--threads", type=int)
    reading.add_argument("--subsets", default=",".join(SUBSETS))
    reading.add_argument("--per-subset", type=int, default=PER_SUBSET)
    scoring = sub.add_parser("score")
    scoring.add_argument("--run", required=True)
    scoring.add_argument("--subsets", default=",".join(SUBSETS))
    args = parser.parse_args()
    if args.command == "run":
        run(args)
    else:
        score(args)


if __name__ == "__main__":
    main()
