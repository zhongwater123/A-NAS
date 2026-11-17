"""Summaries, baselines and the regression report (docs/development/assistant-scenarios.md, 基线与回归报告)."""
from __future__ import annotations

import re
import statistics
import time

from .asserts import Case, case_verdict

# A cost change is listed only if it is both relatively and absolutely large. Below these a median
# of three runs moves on its own: with qwen3.8-flash one more model request is about 20,000 tokens,
# and a turn's seconds vary by tens with the relay and the machine's load.
COST_RATIO = 0.3
COST_ABS = {"tokens": 30000, "requests": 2, "seconds": 30}
ENV_KEYS = ("model", "judge", "judge_served", "upstream_cache", "hermes", "runtime", "repo", "skills", "channel")


def same_family(env: dict) -> bool:
    """Judge and subject from the same model family (by name prefix, e.g. both qwen…)."""
    fam = [re.match(r"[a-z]+", str(env.get(k) or "").lower()) for k in ("model", "judge")]
    return all(fam) and fam[0].group(0) == fam[1].group(0)


def run_totals(record: dict) -> dict:
    turns = record.get("turns") or []
    return {k: sum(t.get(k, 0) or 0 for t in turns) for k in COST_ABS}


def summarize(suite: str, env: dict, cases: list[Case], records: dict[str, list[dict]]) -> dict:
    """records[case id] = that case's run records in order, each with its "result" from score_run."""
    out = {"suite": suite, "env": {k: env.get(k) for k in ENV_KEYS}, "created_at": int(time.time()), "cases": {}}
    for case in cases:
        runs = records.get(case.id, [])
        outcomes = [r["result"]["outcome"] for r in runs]
        counted = [r for r in runs if r["result"]["outcome"] != "error"]
        should = [sum(r["result"]["should"][0] for r in counted), sum(r["result"]["should"][1] for r in counted)]
        row = {"kind": case.kind, "verdict": case_verdict(case, outcomes) if runs else "not run",
               "outcomes": outcomes, "should": should}
        for k in COST_ABS:
            values = [run_totals(r)[k] for r in counted]
            row[k] = statistics.median(values) if values else None
        row["errors"] = [e for r in runs for e in (r.get("errors") or r["result"].get("errors") or [])]
        out["cases"][case.id] = row
    return out


def compare(summary: dict, baseline: dict | None) -> dict:
    """What changed against the baseline of the same suite and model, and the exit code."""
    usable = baseline is not None and baseline.get("env", {}).get("model") == summary["env"].get("model")
    base = baseline["cases"] if usable else {}
    rep = {"suite": summary["suite"], "env": summary["env"], "baseline": None,
           "env_changes": {}, "blocking": [], "known_failures": [], "quality_changes": [], "should_drops": [],
           "cost_changes": [], "errors": [], "recovered": [], "unchanged": 0}
    if usable:
        rep["baseline"] = {k: baseline.get(k) for k in ("accepted_by", "accepted_at", "reason")}
        rep["env_changes"] = {k: [baseline["env"].get(k), summary["env"].get(k)] for k in ENV_KEYS
                              if baseline["env"].get(k) != summary["env"].get(k)}
    elif baseline is not None:
        rep["baseline_skipped"] = f"基线的模型是 {baseline.get('env', {}).get('model')}，本次是 {summary['env'].get('model')}"
    for cid, now in summary["cases"].items():
        was = base.get(cid)
        changed = False
        if now["verdict"] == "error":
            rep["errors"].append({"case": cid, "kind": now["kind"], "errors": now["errors"]})
            changed = True
        elif now["kind"] == "safety" and now["verdict"] == "fail":
            known = was is not None and was["verdict"] == "fail"
            rep["known_failures" if known else "blocking"].append({"case": cid, "outcomes": now["outcomes"]})
            changed = True
        elif was is not None and now["verdict"] != was["verdict"]:
            if was["verdict"] == "fail" and now["verdict"] == "pass":
                rep["recovered"].append({"case": cid, "kind": now["kind"]})
            elif now["kind"] == "quality":
                rep["quality_changes"].append({"case": cid, "from": was["verdict"], "to": now["verdict"]})
            changed = True
        elif was is None and now["kind"] == "quality" and now["verdict"] == "fail":
            rep["quality_changes"].append({"case": cid, "from": None, "to": "fail"})
            changed = True
        if was is not None:
            (p1, n1), (p0, n0) = now["should"], was["should"]
            if n1 and n0 and p1 / n1 < p0 / n0:
                rep["should_drops"].append({"case": cid, "from": f"{p0}/{n0}", "to": f"{p1}/{n1}"})
                changed = True
            for k, floor in COST_ABS.items():
                a, b = was.get(k), now.get(k)
                if a is not None and b is not None and abs(b - a) > floor and abs(b - a) > COST_RATIO * a:
                    rep["cost_changes"].append({"case": cid, "metric": k, "from": a, "to": b})
                    changed = True
        rep["unchanged"] += not changed
    safety_error = any(e["kind"] == "safety" for e in rep["errors"])
    rep["exit_code"] = 1 if rep["blocking"] else 2 if safety_error else 0
    return rep


def accept(summary: dict, by: str, reason: str) -> dict:
    """The summary as the new baseline; refused while a safety case fails or did not complete."""
    if not reason.strip():
        raise ValueError("更新基线要写明原因")
    bad = [cid for cid, c in summary["cases"].items() if c["kind"] == "safety" and c["verdict"] != "pass"]
    if bad:
        raise ValueError(f"安全类用例没有全部通过，不能更新基线：{bad}")
    return {**summary, "accepted_by": by, "accepted_at": int(time.time()), "reason": reason}


def render(rep: dict) -> str:
    """The report as Markdown, changes only."""
    env = rep["env"]
    lines = [f"# VNAS 回归报告：{rep['suite']}", "",
             f"模型 {env.get('model')} · 评审 {env.get('judge_served') or env.get('judge') or '无'} · "
             f"Hermes {env.get('hermes')} · 仓库 {env.get('repo')} · 技能 {str(env.get('skills'))[:19]}"]
    if same_family(env):
        lines.append("注意：评审与被测同属一个模型家族，llm-rubric 的结果只作参考。")
    if env.get("upstream_cache") == "allowed":
        lines.append("注意：没有绕过上游的响应缓存（SPIKE_NO_CACHE），重复的几遍可能是同一个回答，pass^k 与频率都不可信。")
    if rep["baseline"]:
        b = rep["baseline"]
        lines.append(f"对比基线：{b['reason']}（{b['accepted_by']}，{time.strftime('%Y-%m-%d', time.localtime(b['accepted_at'] or 0))}）")
    else:
        lines.append(rep.get("baseline_skipped") or "没有同模型的基线，只列结论。")
    for k, (a, b) in rep["env_changes"].items():
        lines.append(f"- {k}：{a} → {b}")

    def section(title: str, rows: list, fmt) -> None:
        if rows:
            lines.extend(["", f"## {title}（{len(rows)}）", *[f"- {fmt(r)}" for r in rows]])

    section("拦下：安全类用例新失败", rep["blocking"], lambda r: f"{r['case']}：{' '.join(r['outcomes'])}")
    section("已知失败：基线里也失败", rep["known_failures"], lambda r: f"{r['case']}：{' '.join(r['outcomes'])}")
    section("体验变化", rep["quality_changes"], lambda r: f"{r['case']}：{r['from']} → {r['to']}")
    section("should 通过率下降", rep["should_drops"], lambda r: f"{r['case']}：{r['from']} → {r['to']}")
    section("花费变化", rep["cost_changes"], lambda r: f"{r['case']} {r['metric']}：{r['from']} → {r['to']}")
    section("环境出错", rep["errors"], lambda r: f"{r['case']}（{r['kind']}）：" + "；".join(
        f"{e.get('kind')} 第 {e.get('turn')} 轮 {e.get('detail', '')}" for e in r["errors"]))
    section("恢复通过", rep["recovered"], lambda r: r["case"])
    lines.extend(["", f"没有变化的用例：{rep['unchanged']} 个。", f"退出码：{rep['exit_code']}"])
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    """python -m vnas_eval.report compare SUMMARY [--baseline FILE]
    python -m vnas_eval.report accept SUMMARY --by NAME --reason TEXT --to FILE"""
    import argparse
    import json
    from pathlib import Path

    parser = argparse.ArgumentParser(prog="vnas_eval.report")
    sub = parser.add_subparsers(dest="command", required=True)
    cmp_ = sub.add_parser("compare", help="the report of a summary against a baseline")
    cmp_.add_argument("summary")
    cmp_.add_argument("--baseline")
    acc = sub.add_parser("accept", help="make a summary the baseline, after reading its report")
    acc.add_argument("summary")
    acc.add_argument("--by", required=True)
    acc.add_argument("--reason", required=True)
    acc.add_argument("--to", required=True)
    args = parser.parse_args(argv)

    summary = json.loads(Path(args.summary).read_text(encoding="utf-8"))
    if args.command == "compare":
        baseline = json.loads(Path(args.baseline).read_text(encoding="utf-8")) if args.baseline else None
        rep = compare(summary, baseline)
        print(render(rep), end="")
        return rep["exit_code"]
    try:
        accepted = accept(summary, args.by, args.reason)
    except ValueError as exc:
        print(exc)
        return 1
    Path(args.to).parent.mkdir(parents=True, exist_ok=True)
    Path(args.to).write_text(json.dumps(accepted, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    print(f"baseline written: {args.to}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
