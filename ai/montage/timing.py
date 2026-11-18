"""The time and the outside calls of one montage run, in the shape #343 reports them.

Every step of the run goes inside Run.step(id, category), with one of #343's
categories; every call to a model off the host is added with Run.call. Run.write
puts it all in pipeline.json beside the cut:

    seconds:     the wall time from the start of the run
    steps:       each step's id, category, start, seconds, and the container's
                 memory peak when it ended (where a cgroup gives one); a step
                 that raised says so under "failed". Work of another category
                 done inside a step (a check made while planning) is timed
                 with Run.inner and kept under the step's "inner"
    categories:  the seconds per category; steps that ran side by side are each
                 counted whole, so the sum can pass the wall time
    uncovered:   the wall time no step covers, so that nothing goes unaccounted
    calls:       each outside call with its tokens, bytes sent and cost
    external:    their count and totals

report() gives the two cells of #343's table that a run fills itself: the
outside calls and the seconds per step.
"""

import contextlib
import json
import os
import threading
import time

CATEGORIES = ("读取与解码", "切分", "理解", "选择与编排", "精修", "渲染", "检查", "其他")
# The memory peak of every process in the container, page cache included (#343).
PEAK = "/sys/fs/cgroup/memory.peak"
MIB = 1 << 20


class Run:
    def __init__(self, clock=time.monotonic, peak: str = PEAK):
        self.clock, self.peak = clock, peak
        self.began = clock()
        self.steps: list[dict] = []
        self.calls: list[dict] = []
        self.lock = threading.Lock()

    @contextlib.contextmanager
    def step(self, step_id: str, category: str):
        """Times the block as one step; the dict it yields takes the step's own counts."""
        if category not in CATEGORIES:
            raise ValueError(f"类别要是 {'、'.join(CATEGORIES)} 之一：{category}")
        start = self.clock()
        entry = {"id": step_id, "category": category, "start": round(start - self.began, 3)}
        try:
            yield entry
        except BaseException as error:
            entry["failed"] = type(error).__name__
            raise
        finally:
            entry["seconds"] = round(self.clock() - start, 3)
            peak = self._peak()
            if peak is not None:
                entry["memoryPeakMiB"] = peak
            with self.lock:
                self.steps.append(entry)

    def inner(self, entry: dict, category: str, seconds: float) -> None:
        """Counts seconds of the step entry (the dict step yields) as another category."""
        if category not in CATEGORIES:
            raise ValueError(f"类别要是 {'、'.join(CATEGORIES)} 之一：{category}")
        with self.lock:
            inner = entry.setdefault("inner", {})
            inner[category] = round(inner.get(category, 0) + seconds, 3)

    def call(self, step_id: str, provider: str, model: str, *, input_tokens: int, output_tokens: int,
             upload_bytes: int, cost: float, cache_read_tokens: int = 0, cache_write_tokens: int = 0,
             seconds: float = 0.0) -> None:
        """Adds one call to a model off the host: what it was sent and what it cost."""
        with self.lock:
            self.calls.append({"step": step_id, "provider": provider, "model": model,
                               "inputTokens": input_tokens, "outputTokens": output_tokens,
                               "cacheReadTokens": cache_read_tokens, "cacheWriteTokens": cache_write_tokens,
                               "uploadBytes": upload_bytes, "cost": cost, "seconds": round(seconds, 3)})

    def summary(self) -> dict:
        wall = self.clock() - self.began
        with self.lock:
            steps = sorted(self.steps, key=lambda s: s["start"])
            calls = list(self.calls)
        categories = {}
        for step in steps:
            inner = step.get("inner", {})
            for category, seconds in [(step["category"], step["seconds"] - sum(inner.values())), *inner.items()]:
                categories[category] = round(categories.get(category, 0) + seconds, 3)
        covered, reach = 0.0, 0.0
        for step in steps:
            end = step["start"] + step["seconds"]
            covered += max(0.0, end - max(step["start"], reach))
            reach = max(reach, end)
        external = {"count": len(calls),
                    **{key: sum(c[key] for c in calls) for key in
                       ("inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "uploadBytes")},
                    "cost": round(sum(c["cost"] for c in calls), 6)}
        result = {"seconds": round(wall, 3), "steps": steps,
                  "categories": {c: categories[c] for c in CATEGORIES if c in categories},
                  "uncovered": round(max(0.0, wall - covered), 3), "calls": calls, "external": external}
        peak = self._peak()
        if peak is not None:
            result["memoryPeakMiB"] = peak
        return result

    def write(self, path: str, about: dict | None = None) -> dict:
        """Writes the summary to path, with about's keys first, replacing the file whole; returns it."""
        summary = (about or {}) | self.summary()
        partial = path + ".partial"
        with open(partial, "w", encoding="utf-8") as file:
            json.dump(summary, file, ensure_ascii=False, indent=2)
        os.replace(partial, path)
        return summary

    def _peak(self) -> float | None:
        try:
            with open(self.peak, encoding="ascii") as file:
                return round(int(file.read()) / MIB, 1)
        except (OSError, ValueError):
            return None


def report(summary: dict) -> dict:
    """The cells of #343's table for one case: 外部调用 and 各步耗时（秒）."""
    external = summary["external"]
    if external["count"]:
        calls = f"{external['count']} 次 / {external['uploadBytes'] / 1024:.0f} KiB / ${external['cost']:.2f}"
    else:
        calls = "无"
    steps = "；".join(f"{c} {s:.1f}" for c, s in summary["categories"].items())
    if summary["uncovered"] >= 0.05:
        steps += f"；步骤之外 {summary['uncovered']:.1f}"
    return {"外部调用": calls, "各步耗时": steps}
