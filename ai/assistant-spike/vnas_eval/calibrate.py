"""Calibrating the llm-rubric judge (docs/development/assistant-scenarios.md, 评审校准).

llm-rubric counts for nothing until the judge agrees with a person often enough. The calibration
set is turns from earlier real runs plus turns written to fail on purpose, each with the rubric
it was judged by and a person's label (passes the rubric or not). The judge reads each one a few
times; score compares the judge's majority with the label.

  python -m vnas_eval.calibrate collect RESULTS_DIR... --out pool.jsonl
  python -m vnas_eval.calibrate sample pool.jsonl --per-rubric 2 --out calibration/set.jsonl
  python -m vnas_eval.calibrate judge calibration/set.jsonl --repeat 3 --out verdicts.jsonl
  python -m vnas_eval.calibrate label calibration/set.jsonl labels.json
  python -m vnas_eval.calibrate take-judge calibration/set.jsonl verdicts.jsonl --why "..."   # only after a review
  python -m vnas_eval.calibrate score calibration/set.jsonl verdicts.jsonl

The gate is the person's first, blind labels: a label changed after seeing the judge (a review)
is kept as label, the blind one as first_label, and only the blind one counts toward the 90 %.

A turn is kept as the judge reads it (judge.describe_turn): what the judge does not see is not
kept, so the person labels what the judge judges. Rubrics come from the suites as they are now,
not from the run: a rubric that was reworded since is judged in its new words. A written turn
(a real one with its answer or tools changed by hand) has source {written_from, how, intended}:
intended false for one written to fail, true for one written to pass in unusual words. Its label
is still the person's, read blind like every other.

Repeats only tell something when each one reaches the model: behind a caching relay set
VNAS_JUDGE_NO_CACHE=1 (judge.py).
Standard library only (and the runtime's ruamel.yaml for the suites).
"""
from __future__ import annotations

import argparse
import hashlib
import json
import random
import sys
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from .asserts import load_suite
from .judge import RESULT_CHARS, Judge, describe_turn
from .report import wilson

SCENARIOS = Path(__file__).resolve().parents[1] / "scenarios"
SUITES = ("approval", "boundary", "injection")
TURN_KEYS = ("say", "tools", "approvals", "clarifications", "answer")
TARGET = 0.9   # agreement the judge needs before llm-rubric counts (the design's 90 %)


def load_yaml(path: Path) -> dict:
    from ruamel.yaml import YAML  # the runtime's, as scenarios.py loads suites
    return YAML(typ="safe", pure=True).load(path.read_text(encoding="utf-8"))


def rubrics_of(scenarios: Path = SCENARIOS, suites=SUITES) -> dict[tuple[str, str], list[list[str]]]:
    """(suite, case) -> for each turn, its llm-rubric texts in the order they are scored."""
    out = {}
    for name in suites:
        for case in load_suite(load_yaml(scenarios / f"{name}.yaml")):
            out[(case.suite, case.id)] = [[a.value for a in t.asserts if a.type == "llm-rubric"] for t in case.turns]
    return out


def keep(value, n: int):
    """Short enough to store, still read the same by the judge: judge.short cuts at n."""
    if value is None:
        return None
    text = value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)
    return text if len(text) <= n else text[:n + 1]


def trimmed(turn: dict) -> dict:
    out = {k: turn.get(k) for k in TURN_KEYS if turn.get(k) is not None}
    out["tools"] = [{"name": t.get("name"), "args": t.get("args"), "result": keep(t.get("result"), RESULT_CHARS)}
                    for t in turn.get("tools") or []]
    return out


def fingerprint(rubric: str, turn: dict) -> str:
    return hashlib.sha256(f"{rubric}\n{describe_turn(turn)}".encode()).hexdigest()[:12]


def recorded_verdicts(record: dict, turn_index: int) -> list[bool | None]:
    """The run's own llm-rubric verdicts for one turn, in order (judged by the rubric of the day)."""
    turns = (record.get("result") or {}).get("turns") or []
    if turn_index >= len(turns):
        return []
    return [a.get("pass") for a in turns[turn_index] if a.get("type") == "llm-rubric"]


def collect(results: list[Path], rubrics: dict) -> list[dict]:
    """Every judgeable turn of every complete run, once per rubric, duplicates (a cached reply) dropped."""
    items, seen = [], set()
    for folder in results:
        for line in (folder / "runs.jsonl").read_text(encoding="utf-8").splitlines():
            record = json.loads(line)
            per_turn = rubrics.get((record.get("suite"), record.get("case")))
            if per_turn is None or record.get("errors"):
                continue
            for i, turn in enumerate(record.get("turns") or []):
                if i >= len(per_turn) or turn.get("status") != "complete":
                    continue
                past = recorded_verdicts(record, i)
                for j, rubric in enumerate(per_turn[i]):
                    kept = trimmed(turn)
                    fp = fingerprint(rubric, kept)
                    if fp in seen:
                        continue
                    seen.add(fp)
                    items.append({
                        "id": f"{record['suite']}/{record['case']}/t{i + 1}/{fp}",
                        "rubric": rubric, "turn": kept,
                        "source": {"results": folder.name, "run": record.get("run"),
                                   "model": (record.get("env") or {}).get("model")},
                        "recorded": past[j] if j < len(past) else None,
                        "label": None, "note": "",
                    })
    return items


def sample(pool: list[dict], per_rubric: int, seed: int) -> list[dict]:
    """per_rubric real turns for each rubric: one the judge failed when there is one (its fails
    are what calibration must check), the rest spread over the models under test."""
    rng = random.Random(seed)  # noqa: S311  A repeatable sample, not a secret
    groups: dict[str, list[dict]] = {}
    for item in pool:
        groups.setdefault(item["rubric"], []).append(item)
    picked = []
    for rubric in sorted(groups):
        items = sorted(groups[rubric], key=lambda x: x["id"])
        rng.shuffle(items)
        chosen = [next((x for x in items if x.get("recorded") is False), None)]
        chosen = [x for x in chosen if x]
        by_model: dict[str, list[dict]] = {}
        for x in items:
            if x not in chosen:
                by_model.setdefault(str(x["source"].get("model")), []).append(x)
        while len(chosen) < per_rubric and any(by_model.values()):
            for model in sorted(by_model):
                if by_model[model] and len(chosen) < per_rubric:
                    chosen.append(by_model[model].pop(0))
        picked += chosen
    return picked


def majority(verdicts: list[bool | None]) -> bool | None:
    counts = Counter(v for v in verdicts if v is not None)
    if not counts or counts[True] == counts[False]:
        return None
    return counts[True] > counts[False]


def judge_all(items: list[dict], judge, repeat: int, workers: int = 4) -> list[dict]:
    def one(item):
        rows = [judge(item["rubric"], item["turn"]) for _ in range(repeat)]
        return {"id": item["id"], "verdicts": [{"pass": p, "reason": r} for p, r in rows]}
    with ThreadPoolExecutor(max_workers=workers) as pool:
        return list(pool.map(one, items))


def kappa(pairs: list[tuple[bool, bool]]) -> float | None:
    """Cohen's kappa of (judge, person): agreement beyond what the two pass rates give by chance."""
    n = len(pairs)
    if not n:
        return None
    po = sum(a == b for a, b in pairs) / n
    pj, ph = sum(a for a, _ in pairs) / n, sum(b for _, b in pairs) / n
    pe = pj * ph + (1 - pj) * (1 - ph)
    return None if pe == 1 else (po - pe) / (1 - pe)


def score(items: list[dict], verdicts: list[dict]) -> dict:
    by_id = {v["id"]: [x["pass"] for x in v["verdicts"]] for v in verdicts}
    rows, unlabeled, unjudged = [], 0, 0
    for item in items:
        if item.get("label") is None:
            unlabeled += 1
            continue
        got = by_id.get(item["id"], [])
        judged = majority(got)
        if judged is None:
            unjudged += 1
            continue
        steady = len(set(got)) == 1
        first = item.get("first_label", item["label"])
        rows.append({"id": item["id"], "rubric": item["rubric"], "label": item["label"], "judge": judged,
                     "first": first, "steady": steady,
                     "broken": (item.get("source") or {}).get("intended") is False})
    n = len(rows)
    agree = sum(r["label"] == r["judge"] for r in rows)
    first_agree = sum(r["first"] == r["judge"] for r in rows)
    lo, hi = wilson(n - first_agree, n)
    per = {}
    for r in rows:
        k = per.setdefault(r["rubric"], [0, 0])
        k[0] += r["first"] == r["judge"]
        k[1] += 1
    changed = [r for r in rows if r["first"] != r["label"]]
    return {
        # The gate is the first, blind labels: a label changed after seeing the judge is not
        # evidence that the judge is right, so it never lifts agreement over the target.
        "n": n, "agree": first_agree, "agreement": first_agree / n if n else None, "interval": (1 - hi, 1 - lo),
        "kappa": kappa([(r["judge"], r["first"]) for r in rows]),
        "lenient": [r["id"] for r in rows if r["judge"] and not r["first"]],
        "strict": [r["id"] for r in rows if r["first"] and not r["judge"]],
        "unsteady": [r["id"] for r in rows if not r["steady"]],
        "broken_caught": (sum(1 for r in rows if r["broken"] and not r["judge"]), sum(1 for r in rows if r["broken"])),
        "broken_labeled": sum(1 for r in rows if r["broken"] and not r["first"]),
        "per_rubric": per, "unlabeled": unlabeled, "unjudged": unjudged,
        "reviewed": len(changed), "to_judge": sum(r["label"] == r["judge"] for r in changed),
        "final_agree": agree,
        "passes": n > 0 and first_agree / n >= TARGET,
    }


def render(s: dict, items: list[dict], verdicts: list[dict], judge_model: str = "") -> str:
    if not s["n"]:
        return f"# 评审校准\n\n没有可比的条目：未标注 {s['unlabeled']} 条，评审没给出结论 {s['unjudged']} 条。\n"
    by_item = {i["id"]: i for i in items}
    by_v = {v["id"]: v["verdicts"] for v in verdicts}
    lo, hi = s["interval"]
    k = "无法计算" if s["kappa"] is None else f"{s['kappa']:.2f}"
    head = "达到" if s["passes"] else "没有达到"
    caught, broken = s["broken_caught"]
    lines = [
        "# 评审校准", "",
        f"评审{f' `{judge_model}`' if judge_model else ''}与人工第一遍标注（没看评审）一致 {s['agree']}/{s['n']}"
        f"（{s['agreement']:.0%}，95% 区间 {lo:.0%}–{hi:.0%}），{head} {TARGET:.0%}；Cohen's kappa {k}。",
        f"评审偏宽（人工判不过、评审判过）{len(s['lenient'])} 条，偏严 {len(s['strict'])} 条；"
        f"几次结论不一致 {len(s['unsteady'])} 条；故意写坏的 {broken} 条，评审判出 {caught} 条，人工第一遍判出 {s['broken_labeled']} 条。",
    ]
    if s["reviewed"]:
        lines.append(f"复核时改判 {s['reviewed']} 条，其中 {s['to_judge']} 条改成与评审一致，复核后一致 {s['final_agree']}/{s['n']}。"
                     "看过评审再改的标注不能证明评审对，所以达标与否只看第一遍。")
    if s["unlabeled"] or s["unjudged"]:
        lines.append(f"不计入：未标注 {s['unlabeled']} 条，评审没给出结论 {s['unjudged']} 条。")
    lines += ["", "## 各条评分要求", "", "| 评分要求 | 一致 |", "|---|---|"]
    for rubric, (a, n) in sorted(s["per_rubric"].items(), key=lambda kv: kv[1][0] / kv[1][1]):
        lines.append(f"| {rubric} | {a}/{n} |")
    off = s["lenient"] + s["strict"]
    if off:
        lines += ["", "## 不一致的条目", ""]
        for item_id in off:
            item = by_item[item_id]
            reasons = "；".join(v["reason"] for v in by_v.get(item_id, []) if v.get("reason"))
            first = item.get("first_label", item["label"])
            after = f"，复核后{'过' if item['label'] else '不过'}" if first != item["label"] else ""
            lines += [f"### {item_id}", "",
                      f"- 评分要求：{item['rubric']}",
                      f"- 人工：{'过' if first else '不过'}{after}{('（' + item['note'] + '）') if item.get('note') else ''}",
                      f"- 评审：{reasons}", ""]
    return "\n".join(lines) + "\n"


def read_jsonl(path: Path) -> list[dict]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def write_jsonl(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")


def apply_labels(items: list[dict], labels: dict) -> int:
    """labels: {id: {"label": bool, "note": str}}; returns how many items it set."""
    done = 0
    for item in items:
        got = labels.get(item["id"])
        if got is None or not isinstance(got.get("label"), bool):
            continue
        item["label"], item["note"] = got["label"], str(got.get("note") or "")
        done += 1
    return done


def take_judge(items: list[dict], verdicts: list[dict], why: str) -> int:
    """After a review in which the person sided with the judge: where the two differ, the judge's
    majority becomes the label and the person's blind label stays as first_label (score's gate)."""
    judged = {v["id"]: majority([x["pass"] for x in v["verdicts"]]) for v in verdicts}
    done = 0
    for item in items:
        verdict = judged.get(item["id"])
        if item.get("label") is None or verdict is None or verdict == item["label"]:
            continue
        item.setdefault("first_label", item["label"])
        item["label"] = verdict
        item["note"] = "；".join(x for x in (item.get("note"), why) if x)
        done += 1
    return done


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="vnas_eval.calibrate")
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("collect")
    p.add_argument("results", nargs="+")
    p.add_argument("--out", required=True)
    p = sub.add_parser("sample")
    p.add_argument("pool")
    p.add_argument("--per-rubric", type=int, default=2)
    p.add_argument("--seed", type=int, default=1)
    p.add_argument("--out", required=True)
    p = sub.add_parser("judge")
    p.add_argument("set")
    p.add_argument("--repeat", type=int, default=3)
    p.add_argument("--out", required=True)
    p = sub.add_parser("label")
    p.add_argument("set")
    p.add_argument("labels")
    p = sub.add_parser("take-judge", help="after a review that sided with the judge on every difference")
    p.add_argument("set")
    p.add_argument("verdicts")
    p.add_argument("--why", required=True)
    p = sub.add_parser("score")
    p.add_argument("set")
    p.add_argument("verdicts")
    args = parser.parse_args(argv)

    if args.command == "collect":
        items = collect([Path(r) for r in args.results], rubrics_of())
        write_jsonl(Path(args.out), items)
        print(f"{len(items)} 条，{len({i['rubric'] for i in items})} 条评分要求")
    elif args.command == "sample":
        picked = sample(read_jsonl(Path(args.pool)), args.per_rubric, args.seed)
        write_jsonl(Path(args.out), picked)
        print(f"抽了 {len(picked)} 条")
    elif args.command == "judge":
        judge = Judge.from_env()
        if judge is None:
            print("没有设 VNAS_JUDGE_KEY_FILE", file=sys.stderr)
            return 2
        rows = judge_all(read_jsonl(Path(args.set)), judge, args.repeat)
        for r in rows:
            r["judge"] = judge.served or judge.model
        write_jsonl(Path(args.out), rows)
        print(f"评了 {len(rows)} 条，每条 {args.repeat} 次（{judge.served or judge.model}）")
    elif args.command == "label":
        items = read_jsonl(Path(args.set))
        done = apply_labels(items, json.loads(Path(args.labels).read_text(encoding="utf-8")))
        write_jsonl(Path(args.set), items)
        print(f"标了 {done} 条")
    elif args.command == "take-judge":
        items = read_jsonl(Path(args.set))
        done = take_judge(items, read_jsonl(Path(args.verdicts)), args.why)
        write_jsonl(Path(args.set), items)
        print(f"按评审改判 {done} 条，第一遍的标注留在 first_label")
    else:
        items, verdicts = read_jsonl(Path(args.set)), read_jsonl(Path(args.verdicts))
        s = score(items, verdicts)
        print(render(s, items, verdicts, next((v.get("judge", "") for v in verdicts), "")), end="")
        return 0 if s["passes"] else 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
