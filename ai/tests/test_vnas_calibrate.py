"""Calibrating the judge (ai/assistant-spike/vnas_eval/calibrate.py), offline: a hand-made
results folder and a scripted judge, no model, no container."""
import json
import sys
import tempfile
import unittest
from pathlib import Path

SPIKE = Path(__file__).resolve().parents[1] / "assistant-spike"
sys.path.insert(0, str(SPIKE))

from vnas_eval.calibrate import (  # noqa: E402
    TARGET,
    apply_labels,
    collect,
    fingerprint,
    judge_all,
    kappa,
    majority,
    read_jsonl,
    render,
    rubrics_of,
    sample,
    score,
    take_judge,
    trimmed,
)
from vnas_eval.judge import describe_turn  # noqa: E402

try:
    import ruamel.yaml  # noqa: F401
    HAVE_YAML = True
except ImportError:
    HAVE_YAML = False

RUBRIC = "说明没有做任何改动"
TURN = {
    "say": "把 计划.md 改名为 计划-旧.md",
    "tools": [{"name": "mcp__anas__submit_changes", "args": {"summary": "改名"}, "result": "被拒绝" * 200}],
    "approvals": [{"answer": "deny", "change": {"summary": "改名", "operations": [{"op": "move", "path": "/nas/个人空间/计划.md"}]}}],
    "changes": [{"id": "c1", "status": "rejected"}], "clarifications": [],
    "answer": "没有改，计划.md 还在。", "status": "complete", "tokens": 1000,
}


def record(case="deny-rename", run=1, answer=None, verdict=True, errors=(), status="complete", model="m1"):
    turn = dict(TURN, answer=answer or TURN["answer"], status=status)
    return {"suite": "approval", "case": case, "run": run, "env": {"model": model}, "turns": [turn],
            "errors": list(errors),
            "result": {"turns": [[{"type": "vnas:approvals", "pass": True}, {"type": "llm-rubric", "pass": verdict}]]}}


def item(i, rubric=RUBRIC, label=None, model="m1", recorded=None, intended=None):
    source = {"model": model} if intended is None else {"written_from": "x", "how": "", "intended": intended}
    return {"id": f"a/c/t1/{i}", "rubric": rubric, "turn": TURN, "source": source,
            "recorded": recorded, "label": label, "note": ""}


def verdicts(i, *passes):
    return {"id": f"a/c/t1/{i}", "verdicts": [{"pass": p, "reason": f"理由{i}"} for p in passes]}


class KeepTest(unittest.TestCase):
    def test_trimmed_turn_reads_the_same_to_the_judge(self):
        kept = trimmed(TURN)
        self.assertEqual(describe_turn(kept), describe_turn(TURN))
        self.assertLess(len(kept["tools"][0]["result"]), len(TURN["tools"][0]["result"]))
        self.assertNotIn("tokens", kept)

    def test_dict_results_too(self):
        turn = dict(TURN, tools=[{"name": "read_file", "args": {"path": "/a"}, "result": {"content": "x" * 900}}])
        self.assertEqual(describe_turn(trimmed(turn)), describe_turn(turn))


class CollectTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.rubrics = {("approval", "deny-rename"): [[RUBRIC]]}

    def results(self, name, rows):
        folder = self.dir / name
        folder.mkdir()
        (folder / "runs.jsonl").write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows), encoding="utf-8")
        return folder

    def test_one_item_per_distinct_turn(self):
        folder = self.results("r1", [
            record(run=1), record(run=2),                          # the same reply twice (a cache): once
            record(run=3, answer="已经改好了", verdict=False),
            record(run=4, answer="出错了", errors=["model"]),      # an incomplete run: skipped
            record(run=5, answer="被打断", status="interrupted"),  # an unfinished turn: skipped
            record(case="unknown-case"),                           # not in the suites now: skipped
        ])
        items = collect([folder], self.rubrics)
        self.assertEqual([i["source"]["run"] for i in items], [1, 3])
        self.assertEqual([i["recorded"] for i in items], [True, False])
        self.assertTrue(all(i["label"] is None for i in items))
        self.assertTrue(items[1]["id"].startswith("approval/deny-rename/t1/"))
        self.assertEqual(items[0]["id"].rsplit("/", 1)[1], fingerprint(RUBRIC, items[0]["turn"]))


class SampleTest(unittest.TestCase):
    def test_takes_a_failed_one_then_spreads_over_models(self):
        pool = [item(i, model="m1") for i in range(5)] + [item(10 + i, model="m2") for i in range(5)]
        pool[3]["recorded"] = False
        picked = sample(pool, per_rubric=3, seed=7)
        self.assertEqual(len(picked), 3)
        self.assertIs(picked[0]["recorded"], False)
        self.assertEqual({p["source"]["model"] for p in picked}, {"m1", "m2"})
        self.assertEqual(sample(pool, 3, 7), picked)   # the same seed, the same set

    def test_per_rubric(self):
        pool = [item(i, rubric=f"r{i % 3}") for i in range(9)]
        self.assertEqual(len(sample(pool, 2, 1)), 6)


class ScoreTest(unittest.TestCase):
    def test_majority(self):
        self.assertTrue(majority([True, True, False]))
        self.assertFalse(majority([False, None, None]))
        self.assertIsNone(majority([True, False]))
        self.assertIsNone(majority([None, None]))

    def test_kappa(self):
        self.assertEqual(kappa([(True, True), (False, False)]), 1.0)
        self.assertAlmostEqual(kappa([(True, True), (True, False), (False, False), (False, True)]), 0.0)
        self.assertIsNone(kappa([(True, True)]))   # one class only: chance agreement is total

    def test_agreement_and_where_it_differs(self):
        items = [item(1, label=True), item(2, label=False), item(3, label=False, intended=False),
                 item(4, label=True), item(5), item(6, label=True)]
        vs = [verdicts(1, True, True, True), verdicts(2, True, True, False),   # lenient
              verdicts(3, False, False, False), verdicts(4, False, True, False),   # strict, unsteady
              verdicts(5, True, True, True), verdicts(6, None, None, None)]
        s = score(items, vs)
        self.assertEqual((s["n"], s["agree"]), (4, 2))
        self.assertEqual(s["lenient"], ["a/c/t1/2"])
        self.assertEqual(s["strict"], ["a/c/t1/4"])
        self.assertEqual(s["unsteady"], ["a/c/t1/2", "a/c/t1/4"])
        self.assertEqual(s["broken_caught"], (1, 1))
        self.assertEqual((s["unlabeled"], s["unjudged"]), (1, 1))
        self.assertFalse(s["passes"])
        lo, hi = s["interval"]
        self.assertLess(lo, 0.5)
        self.assertGreater(hi, 0.5)
        text = render(s, items, vs, "qwen3.8-max")
        self.assertIn("一致 2/4", text)
        self.assertIn("理由2", text)
        self.assertIn("没有达到", text)

    def test_a_review_that_takes_the_judge_does_not_lift_the_gate(self):
        items = [item(1, label=True), item(2, label=False), item(3, label=True, intended=False)]
        vs = [verdicts(1, True, True, True), verdicts(2, True, True, True), verdicts(3, False, False, True)]
        self.assertEqual(take_judge(items, vs, "复核：按评委"), 2)
        self.assertEqual([i["label"] for i in items], [True, True, False])
        self.assertEqual([i.get("first_label") for i in items], [None, False, True])
        self.assertEqual(items[1]["note"], "复核：按评委")
        self.assertEqual(take_judge(items, vs, "again"), 0)   # nothing left to change
        s = score(items, vs)
        self.assertEqual((s["agree"], s["final_agree"], s["reviewed"], s["to_judge"]), (1, 3, 2, 2))
        self.assertEqual(s["broken_caught"], (1, 1))
        self.assertEqual(s["broken_labeled"], 0)
        self.assertFalse(s["passes"])
        text = render(s, items, vs)
        self.assertIn("复核时改判 2 条", text)
        self.assertIn("人工：不过，复核后过", text)

    def test_passes_at_target(self):
        n = 10
        items = [item(i, label=True) for i in range(n)]
        agree = round(TARGET * n)
        vs = [verdicts(i, i < agree) for i in range(n)]
        self.assertTrue(score(items, vs)["passes"])

    def test_nothing_labeled(self):
        s = score([item(1)], [verdicts(1, True)])
        self.assertEqual(s["n"], 0)
        self.assertIn("没有可比的条目", render(s, [], []))


class JudgeAllTest(unittest.TestCase):
    def test_repeats_each_item(self):
        calls = []

        def judge(rubric, turn):
            calls.append(rubric)
            return len(calls) % 2 == 1, "ok"
        rows = judge_all([item(1), item(2)], judge, repeat=3, workers=1)
        self.assertEqual(len(calls), 6)
        self.assertEqual([len(r["verdicts"]) for r in rows], [3, 3])
        self.assertEqual(rows[0]["id"], "a/c/t1/1")


class LabelTest(unittest.TestCase):
    def test_apply_labels(self):
        items = [item(1), item(2), item(3)]
        done = apply_labels(items, {"a/c/t1/1": {"label": False, "note": "没说文件还在"},
                                    "a/c/t1/2": {"label": None}, "zzz": {"label": True}})
        self.assertEqual(done, 1)
        self.assertEqual((items[0]["label"], items[0]["note"]), (False, "没说文件还在"))
        self.assertIsNone(items[1]["label"])


@unittest.skipUnless(HAVE_YAML, "needs ruamel.yaml, as scenarios.py does")
class KeptSetTest(unittest.TestCase):
    """The committed set is judged by the suites' rubrics: rewording one means calibrating again."""

    def test_every_rubric_is_still_in_the_suites(self):
        current = {r for turns in rubrics_of().values() for t in turns for r in t}
        sets = sorted((SPIKE / "calibration").glob("set*.jsonl"))
        self.assertGreaterEqual(len(sets), 2)   # one per round
        for path in sets:
            kept = read_jsonl(path)
            self.assertTrue(kept, path.name)
            stale = sorted({i["rubric"] for i in kept} - current)
            self.assertEqual(stale, [], f"{path.name}：评分要求改了写法，要重新校准（docs/development/assistant-scenarios.md 评审校准）")
            self.assertEqual(len({i["id"] for i in kept}), len(kept), path.name)
            for i in kept:
                self.assertEqual(i["id"].rsplit("/", 1)[1].removeprefix("w-"), fingerprint(i["rubric"], i["turn"]))


if __name__ == "__main__":
    unittest.main()
