"""Per-case and per-plan numbers (docs/specs/video-edit-evaluation.md#指标). Standard library only."""

import collections
import statistics

from eval.video_edit import trace


def external_totals(calls):
    cost = collections.defaultdict(float)
    for call in calls:
        cost[call["currency"] or "未写币种"] += call["cost"]
    return {
        "calls": len(calls),
        "upload_bytes": sum(call["upload_bytes"] for call in calls),
        "input_tokens": sum(call["input_tokens"] for call in calls),
        "output_tokens": sum(call["output_tokens"] for call in calls),
        "seconds": sum(call["seconds"] for call in calls),
        "cost": {currency: round(amount, 6) for currency, amount in sorted(cost.items()) if amount},
    }


def case_metrics(run, spans, stages, source_seconds):
    wall = run["wall_seconds"]
    return {
        "ok": run["ok"],
        "failure": run["failure"],
        "wall_seconds": wall,
        "cpu_seconds": run["cpu_seconds"],
        "average_cores": run["cpu_seconds"] / wall if wall else None,
        "memory_peak_bytes": run["memory_peak_bytes"],
        "anon_peak_bytes": run["anon_peak_bytes"],
        "file_peak_bytes": run["file_peak_bytes"],
        "oom_kill": run["oom_kill"],
        "disk_peak_bytes": run["disk_peak_bytes"],
        "net_sent_bytes": run["net_sent_bytes"],
        "net_received_bytes": run["net_received_bytes"],
        "source_seconds": source_seconds,
        "processing_ratio": wall / source_seconds if source_seconds else None,
        "stage_seconds": trace.stage_seconds(spans, stages) if spans is not None else {},
        "external": external_totals(trace.external_calls(spans, stages) if spans is not None else []),
    }


def relative(case, baseline):
    """Seconds against the baseline on the same device and case; None unless both succeeded."""
    if case and baseline and case["ok"] and baseline["ok"] and baseline["wall_seconds"]:
        return case["wall_seconds"] / baseline["wall_seconds"]
    return None


def category_seconds(pipeline, cases):
    by_stage = {stage["id"]: stage["category"] for stage in pipeline["stages"]}
    totals = collections.defaultdict(float)
    for case in cases.values():
        for stage, seconds in case["stage_seconds"].items():
            totals[by_stage[stage]] += seconds
    return dict(totals)


def median(values):
    """The median of the known values; None when there are none."""
    values = [value for value in values if value is not None]
    return statistics.median(values) if values else None


def summary(cases, relatives):
    """cases: {case id: case_metrics}; relatives: {case id: relative time or None}."""
    external = external_totals([])
    for case in cases.values():
        for key in ("calls", "upload_bytes", "input_tokens", "output_tokens", "seconds"):
            external[key] += case["external"][key]
        for currency, amount in case["external"]["cost"].items():
            external["cost"][currency] = round(external["cost"].get(currency, 0) + amount, 6)
    peaks = [case["memory_peak_bytes"] for case in cases.values()]
    return {
        "cases": len(cases),
        "ok": sum(case["ok"] for case in cases.values()),
        "memory_peak_max": max(peaks, default=None),
        "memory_peak_median": median(peaks),
        "anon_peak_max": max((case["anon_peak_bytes"] for case in cases.values()), default=None),
        "oom_cases": sum(bool(case["oom_kill"]) for case in cases.values()),
        "disk_peak_max": max((case["disk_peak_bytes"] for case in cases.values()), default=None),
        "wall_total": sum(case["wall_seconds"] for case in cases.values()),
        "cpu_total": sum(case["cpu_seconds"] for case in cases.values()),
        "relative_median": median(relatives.values()),
        "external": external,
    }
