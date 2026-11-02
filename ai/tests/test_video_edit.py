"""The video edit evaluation harness's parts that need no Docker (standard library only).

docs/specs/video-edit-evaluation.md describes what they measure and check.
"""

import argparse
import contextlib
import importlib
import io
import json
import os
import random
import sys
import tarfile
import tempfile
import unittest

from eval.video_edit import blind, checks, manifest, metrics, report, sampler, trace

host = importlib.import_module("eval.video_edit.__main__")


def span(name, span_id, parent="", start=0.0, end=1.0, **attributes):
    values = []
    for key, value in attributes.items():
        key = "anas.external." + key
        if isinstance(value, str):
            values.append({"key": key, "value": {"stringValue": value}})
        elif isinstance(value, int):
            values.append({"key": key, "value": {"intValue": str(value)}})
        else:
            values.append({"key": key, "value": {"doubleValue": value}})
    return {"name": name, "spanId": span_id, "parentSpanId": parent, "startTimeUnixNano": str(int(start * 1e9)),
            "endTimeUnixNano": str(int(end * 1e9)), "attributes": values}


def document(*spans):
    return {"resourceSpans": [{"scopeSpans": [{"spans": list(spans)}]}]}


def stage(stage_id, category="other", runs_on="nas-cpu", sends_out=()):
    return {"id": stage_id, "category": category, "does": "做", "inputs": ["素材"], "outputs": ["结果"], "uses": ["工具"],
            "runs_on": runs_on, "sends_out": list(sends_out), "on_failure": "用例失败"}


PIPELINE = {
    "version": 1, "plan": "A", "summary": "测试用", "entry": ["run"],
    "stages": [stage("describe", "understand", "cloud", [{"data": "抽帧", "to": "云端模型"}]), stage("render", "render")],
}


def run_json(**overrides):
    run = {"ok": True, "failure": None, "wall_seconds": 100.0, "cpu_seconds": 400.0, "memory_peak_bytes": 2 << 30,
           "anon_peak_bytes": 1 << 30, "file_peak_bytes": 1 << 29, "oom_kill": 0, "disk_peak_bytes": 1 << 28,
           "net_sent_bytes": 0, "net_received_bytes": 0, "outputs": {"trace.json": 10}}
    run.update(overrides)
    return run


class TraceTest(unittest.TestCase):
    def setUp(self):
        self.spans = trace.parse(document(
            span("describe", "a", start=0, end=10),
            span("call", "b", "a", start=1, end=4, target="cloud", service="vl", upload_bytes=2048, input_tokens=100, cost=0.5, currency="CNY"),
            span("describe", "c", "a", start=5, end=6),
            span("render", "d", start=10, end=30),
            span("warmup", "e", start=0, end=1),
        ))
        self.stages = ["describe", "render"]

    def test_counts_a_stage_inside_itself_once(self):
        self.assertEqual(trace.stage_seconds(self.spans, self.stages), {"describe": 10.0, "render": 20.0})

    def test_nested_spans_belong_to_their_stage(self):
        self.assertEqual(trace.undeclared(self.spans, self.stages), ["warmup"])
        call, = trace.external_calls(self.spans, self.stages)
        self.assertEqual((call["stage"], call["target"], call["upload_bytes"], call["input_tokens"], call["cost"]),
                         ("describe", "cloud", 2048, 100, 0.5))
        self.assertEqual(call["seconds"], 3.0)


class ChecksTest(unittest.TestCase):
    def test_reports_what_the_pipeline_does_not_declare(self):
        local = {**PIPELINE, "stages": [stage("render", "render")]}
        cases = {
            "case-01": {"run": run_json(net_sent_bytes=5 << 20), "timeline_ok": True,
                        "spans": trace.parse(document(span("render", "a"), span("call", "b", "a", target="cloud", service="vl")))},
            "case-02": {"run": run_json(), "timeline_ok": False, "spans": trace.parse(document(span("extra", "a")))},
            "case-03": {"run": run_json(), "timeline_ok": None, "spans": None},
        }
        found = checks.findings(local, cases)
        self.assertIn("步骤 render 声明的运行位置是宿主机 CPU，却有外部调用（云端，vl）：case-01", found)
        self.assertIn("没有声明外部计算，容器却发送了网络数据：case-01（5.0 MiB）", found)
        self.assertIn("未声明的步骤 extra：case-02", found)
        self.assertIn("剪辑决策无法读取：case-02", found)
        self.assertIn("trace.json 无法读取：case-03", found)

    def test_a_declared_external_stage_is_quiet(self):
        cases = {"case-01": {"run": run_json(net_sent_bytes=50 << 20), "timeline_ok": True, "spans": trace.parse(document(
            span("describe", "a"), span("call", "b", "a", target="cloud", service="vl"), span("render", "c")))}}
        self.assertEqual(checks.findings(PIPELINE, cases), [])

    def test_a_stage_no_case_shows(self):
        cases = {"case-01": {"run": run_json(), "timeline_ok": True, "spans": trace.parse(document(span("render", "a")))}}
        self.assertEqual(checks.findings(PIPELINE, cases), ["步骤 describe 在 1 个有 trace 的用例里都没有出现"])


class MetricsTest(unittest.TestCase):
    def test_relative_time_needs_both_runs(self):
        spans = trace.parse(document(span("render", "a", end=60)))
        case = metrics.case_metrics(run_json(), spans, ["describe", "render"], 50.0)
        self.assertEqual(case["processing_ratio"], 2.0)
        self.assertEqual(case["average_cores"], 4.0)
        baseline = metrics.case_metrics(run_json(wall_seconds=25.0), None, ["render"], 50.0)
        self.assertEqual(metrics.relative(case, baseline), 4.0)
        self.assertIsNone(metrics.relative(case, {**baseline, "ok": False}))
        self.assertEqual(metrics.category_seconds(PIPELINE, {"case-01": case}), {"understand": 0.0, "render": 60.0})

    def test_summary_adds_external_costs_per_currency(self):
        spans = trace.parse(document(span("describe", "a"), span("c", "b", "a", target="cloud", cost=0.25, currency="CNY")))
        cases = {name: metrics.case_metrics(run_json(), spans, ["describe"], 10.0) for name in ("case-01", "case-02")}
        total = metrics.summary(cases, {"case-01": 2.0, "case-02": None})
        self.assertEqual((total["ok"], total["relative_median"], total["external"]["calls"]), (2, 2.0, 2))
        self.assertEqual(total["external"]["cost"], {"CNY": 0.5})


class SamplerTest(unittest.TestCase):
    def measure(self, script, expect=("output.mp4",)):
        with tempfile.TemporaryDirectory() as root:
            cgroup, out, tmp = (os.path.join(root, name) for name in ("cgroup", "out", "tmp"))
            for path in (cgroup, out, tmp):
                os.makedirs(path)
            for name, text in {"memory.current": "1000\n", "memory.peak": "5000\n", "memory.stat": "anon 300\nfile 200\n",
                               "cpu.stat": "usage_usec 2500000\nuser_usec 1\n", "memory.events": "oom 0\noom_kill 0\n"}.items():
                with open(os.path.join(cgroup, name), "w", encoding="ascii") as file:
                    file.write(text)
            run = sampler.measure([sys.executable, "-c", script, out], out, tmp, 60, expect, interval=0.05, cgroup=cgroup)
            with open(os.path.join(out, "harness", "run.json"), encoding="utf-8") as file:
                self.assertEqual(json.load(file), run)
            return run

    def test_reads_the_cgroup(self):
        run = self.measure("import sys, time; time.sleep(0.2); open(sys.argv[1] + '/output.mp4', 'wb').write(b'x' * 10)")
        self.assertTrue(run["ok"])
        self.assertEqual((run["memory_peak_bytes"], run["cpu_seconds"], run["anon_peak_bytes"]), (5000, 2.5, 300))
        self.assertEqual(run["outputs"], {"output.mp4": 10})
        self.assertGreaterEqual(run["disk_peak_bytes"], 10)

    def test_a_failed_run_says_why(self):
        self.assertEqual(self.measure("raise SystemExit(3)")["failure"], "退出码 3")
        self.assertEqual(self.measure("pass")["failure"], "缺少 output.mp4")

    def test_keyed_reads_counters(self):
        self.assertEqual(sampler.keyed("anon 5\nfile 7\nbad x\n"), {"anon": 5, "file": 7})


class ManifestTest(unittest.TestCase):
    def test_finds_missing_and_changed_files(self):
        with tempfile.TemporaryDirectory() as root:
            for name, data in (("case-01.json", b"{}"), ("case-01-1.mp4", b"video"), ("case-02.json", b"{}")):
                with open(os.path.join(root, name), "wb") as file:
                    file.write(data)
            built = manifest.build(root)
            self.assertEqual(manifest.case_ids(built), ["case-01", "case-02"])
            self.assertEqual(manifest.problems(root, built), [])
            with open(os.path.join(root, "case-01-1.mp4"), "wb") as file:
                file.write(b"other")
            os.remove(os.path.join(root, "case-02.json"))
            self.assertEqual(manifest.problems(root, built), ["case-01-1.mp4 与清单不符", "缺少 case-02.json"])


class BundleTest(unittest.TestCase):
    def write_runs(self, runs):
        for plan in ("A", "baseline"):
            folder = os.path.join(runs, plan)
            os.makedirs(os.path.join(folder, "cases", "case-01", "harness"))
            host.write_json(os.path.join(folder, "session.json"), {
                "method_version": "v1", "harness_commit": "abc", "dev": False, "fingerprint": "f", "manifest_sha256": "m",
                "cases": ["case-01"], "machine": {"cpu": "x"}})
            host.write_json(os.path.join(folder, "prepare.json"), {"external": False})
            host.write_json(os.path.join(folder, "cases", "case-01", "harness", "run.json"), run_json())
        os.makedirs(os.path.join(runs, "A", ".tmp"))

    def test_a_bundle_round_trips_and_a_changed_file_fails(self):
        with tempfile.TemporaryDirectory() as root:
            runs, bundle, out = (os.path.join(root, name) for name in ("runs", "A.tar", "out"))
            self.write_runs(runs)
            with contextlib.redirect_stdout(io.StringIO()):
                host.command_pack(argparse.Namespace(runs=runs, out=bundle))
            packed, session = host.unpack(bundle, out)
            self.assertEqual((packed["plan"], session["harness_commit"]), ("A", "abc"))
            self.assertNotIn("A/.tmp", " ".join(packed["files"]))
            with tarfile.open(bundle, "a") as archive:
                archive.add(os.path.join(runs, "A", "session.json"), "A/prepare.json")
            with self.assertRaises(SystemExit):
                host.unpack(bundle, os.path.join(root, "again"))

    def test_a_plan_and_baseline_from_different_sources_do_not_pack(self):
        with tempfile.TemporaryDirectory() as root:
            runs = os.path.join(root, "runs")
            self.write_runs(runs)
            path = os.path.join(runs, "baseline", "session.json")
            session = host.read_json(path)
            host.write_json(path, {**session, "fingerprint": "other"})
            with self.assertRaises(SystemExit):
                host.command_pack(argparse.Namespace(runs=runs, out=os.path.join(root, "A.tar")))


class BlindTest(unittest.TestCase):
    def test_review_round_trip(self):
        key = blind.assign(["case-01", "case-02"], ["A", "B"], random.Random(1))  # noqa: S311  a repeatable shuffle for the test
        self.assertTrue(all(sorted(labels.values()) == ["A", "B"] for labels in key.values()))
        with tempfile.TemporaryDirectory() as root:
            outputs = {}
            for plan in ("A", "B"):
                outputs[plan] = {}
                for case in key:
                    path = os.path.join(root, f"{plan}-{case}.mp4")
                    with open(path, "wb") as file:
                        file.write(plan.encode())
                    outputs[plan][case] = path
            folder = os.path.join(root, "review")
            blind.write_review(key, outputs, folder)
            rows = blind.read_rows(os.path.join(folder, "review.csv"))
            self.assertEqual(len(rows), 4)
            with open(os.path.join(folder, rows[0]["file"]), "rb") as file:
                self.assertEqual(file.read().decode(), key[rows[0]["case"]][rows[0]["label"]])
        for row in rows:
            row["更好的一条"] = "X"
            row["评分"] = "4" if key[row["case"]][row["label"]] == "A" else "2"
        unblinded, choices = blind.unblind(key, rows)
        self.assertEqual(choices, ["更好的一条"])
        self.assertTrue(all(row["更好的一条"] == key[row["case"]]["X"] for row in unblinded))
        lines = blind.tally(unblinded, ["A", "B"], choices)
        self.assertIn("- 评分 平均：方案 A 4.00（2 条），方案 B 2.00（2 条）", lines)
        chosen_a = sum(labels["X"] == "A" for labels in key.values())
        self.assertIn(f"- 更好的一条：方案 A {chosen_a} 个，方案 B {2 - chosen_a} 个", lines)


class ReportTest(unittest.TestCase):
    def result(self, name, ok=True):
        spans = trace.parse(document(span("render", "a", end=50)))
        cases = {case: metrics.case_metrics(run_json(ok=ok, failure=None if ok else "超时"), spans, ["describe", "render"], 30.0)
                 for case in ("case-01", "case-02")}
        relatives = dict.fromkeys(cases, 2.0)
        return {
            "name": name, "pipeline": {**PIPELINE, "plan": name},
            "session": {"machine": {"cpu": "cpu", "cpus": 8, "memory_bytes": 8 << 30, "os": "Debian", "kernel": "6.12", "docker": "29"}},
            "env": {"env_bytes": 1 << 30, "plan_bytes": 1 << 20, "models": []},
            "cases": cases, "relative": relatives, "offline": {"case-01": True, "case-02": False}, "findings": ["未声明的步骤 x：case-01"],
            "summary": metrics.summary(cases, relatives), "categories": metrics.category_seconds(PIPELINE, cases),
        }

    def test_renders_the_card_and_the_comparison(self):
        a, b = self.result("A"), self.result("B", ok=False)
        card = report.render_card(a)
        self.assertIn("s0 --> s1", card)
        self.assertIn("| `describe` | 理解 |", card)
        text = report.render_report(a, b, {"case-01": {"provider": "A", "source_seconds": 30.0},
                                            "case-02": {"provider": "B", "source_seconds": 30.0}},
                                    {"method_version": "v1", "harness_commit": "c" * 40, "fingerprint": "f" * 64, "manifest_sha256": "m" * 64})
        self.assertIn("失败：超时", text)
        self.assertIn("| 断网能出片 | 1/2 | 1/2 |", text)
        self.assertIn("方案 A 提供的 1 个", text)


if __name__ == "__main__":
    unittest.main()
