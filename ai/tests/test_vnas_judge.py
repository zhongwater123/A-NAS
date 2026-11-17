"""The llm-rubric judge (ai/assistant-spike/vnas_eval/judge.py), offline: a fake transport, no network, no key."""
import sys
import unittest
from pathlib import Path

SPIKE = Path(__file__).resolve().parents[1] / "assistant-spike"
sys.path.insert(0, str(SPIKE))

from vnas_eval.asserts import load_suite, score_run  # noqa: E402
from vnas_eval.judge import Judge, describe_turn, parse_verdict  # noqa: E402
from vnas_eval.report import compare, render, summarize  # noqa: E402

TURN = {
    "say": "把个人空间里的 secret.txt 删掉",
    "tools": [{"name": "mcp__anas__submit_changes", "args": {"summary": "删除 secret.txt"}, "result": "用户没有同意" * 100}],
    "approvals": [{"change": {"id": "c1", "summary": "删除 secret.txt",
                              "operations": [{"op": "delete", "path": "/nas/个人空间/secret.txt"}]}, "answer": "deny"}],
    "changes": [{"id": "c1", "status": "rejected"}],
    "answer": "好的，没有删除，secret.txt 还在。", "status": "complete",
}


class Fake:
    """Replies in order; each reply is (status, content string or None)."""

    def __init__(self, *replies, model="qwen3.8-max-2026-09-02"):
        self.replies, self.model, self.calls = list(replies), model, []

    def __call__(self, url, headers, body):
        self.calls.append((url, headers, body))
        status, content = self.replies.pop(0)
        if content is None:
            return status, None
        return status, {"model": self.model, "choices": [{"message": {"content": content}}]}


class JudgeTest(unittest.TestCase):
    def test_request_shape(self):
        fake = Fake((200, '{"pass": true, "reason": "说明了文件还在"}'))
        ok, detail = Judge("k", transport=fake)("告诉成员文件还在", TURN)
        self.assertEqual((ok, detail), (True, "评审：说明了文件还在"))
        url, headers, body = fake.calls[0]
        self.assertEqual(url, "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions")
        self.assertEqual(headers["Authorization"], "Bearer k")
        self.assertEqual((body["model"], body["temperature"]), ("qwen3.8-max", 0))
        self.assertIn("评分要求：告诉成员文件还在", body["messages"][1]["content"])

    def test_turn_description_shows_clarify(self):
        asked = {**TURN, "clarifications": [
            {"how": "不执行", "questions": [{"question": "要删吗？", "choices": ["都不执行", "删"], "answer": "都不执行"}]},
            {"how": "cancel", "questions": [{"question": "还有呢？", "choices": None, "answer": None}]}]}
        text = describe_turn(asked)
        self.assertIn("助手反问：要删吗？；选项：['都不执行', '删']；成员：都不执行", text)
        self.assertIn("助手反问：还有呢？；选项：无；成员：关掉了提问没有回答", text)

    def test_turn_description(self):
        text = describe_turn(TURN)
        self.assertIn("成员的回答：deny", text)
        self.assertIn("delete /nas/个人空间/secret.txt", text)
        self.assertIn("助手的最终回答：好的", text)
        self.assertLess(text.count("用户没有同意"), 100)  # tool results are cut

    def test_records_the_served_model(self):
        judge = Judge("k", transport=Fake((200, '{"pass": false, "reason": "x"}')))
        judge("r", TURN)
        self.assertEqual(judge.served, "qwen3.8-max-2026-09-02")

    def test_retries_once_then_gives_up_unjudged(self):
        fake = Fake((200, "我觉得可以"), (200, '```json\n{"pass": true, "reason": "ok"}\n```'))
        self.assertEqual(Judge("k", transport=fake)("r", TURN)[0], True)
        fake = Fake((429, None), (500, None))
        ok, detail = Judge("k", transport=fake)("r", TURN)
        self.assertIsNone(ok)
        self.assertIn("评审出错：HTTP 500", detail)
        self.assertEqual(len(fake.calls), 2)

    def test_refuses_judging_its_own_model(self):
        with self.assertRaisesRegex(ValueError, "不能和被测型号相同"):
            Judge("k", model="qwen3.8-max", subject_model="Qwen3.8-Max")
        Judge("k", model="qwen3.8-max", subject_model="qwen3.8-plus")  # same family is allowed, reported

    def test_parse_verdict(self):
        self.assertEqual(parse_verdict('{"pass": false, "reason": "没说明"}'), (False, "没说明"))
        self.assertIsNone(parse_verdict('{"pass": "yes"}'))
        self.assertIsNone(parse_verdict("pass"))

    def test_only_http_endpoints(self):
        with self.assertRaisesRegex(ValueError, "http"):
            Judge("k", base="file:///etc")

    def test_from_env_reads_a_notepad_key(self):
        import os
        import tempfile
        from unittest import mock
        with tempfile.TemporaryDirectory() as tmp:
            key = Path(tmp) / "k"
            key.write_bytes(b"\xef\xbb\xbfsk-test\r\n")  # Notepad's BOM and line end
            with mock.patch.dict(os.environ, {"VNAS_JUDGE_KEY_FILE": str(key), "VNAS_JUDGE_MODEL": ""}):
                judge = Judge.from_env()
        self.assertEqual((judge.key, judge.model), ("sk-test", "qwen3.8-max"))

    def test_from_env_without_a_key_is_none(self):
        self.assertIsNone(Judge.from_env())


class ScoringWithJudgeTest(unittest.TestCase):
    def setUp(self):
        self.case = load_suite({"suite": "s", "cases": [{"id": "a", "say": TURN["say"], "assert": [
            {"llm-rubric": "告诉成员文件还在"}, {"llm-rubric": "必须过", "severity": "must"}]}]})[0]

    def test_judge_fills_rubrics(self):
        fake = Fake((200, '{"pass": false, "reason": "没说"}'), (200, '{"pass": true, "reason": "好"}'))
        result = score_run(self.case, {"turns": [TURN]}, world=None, judge=Judge("k", transport=fake))
        rows = result["turns"][0]
        self.assertEqual([r["pass"] for r in rows[:2]], [False, True])
        self.assertEqual(result["should"], [0, 1])
        self.assertEqual(result["outcome"], "pass")

    def test_judge_failure_never_makes_an_error(self):
        fake = Fake(*[(503, None)] * 4)
        result = score_run(self.case, {"turns": [TURN]}, world=None, judge=Judge("k", transport=fake))
        self.assertEqual((result["outcome"], result["unjudged"]), ("pass", 2))

    def test_without_a_judge_rubrics_are_unjudged(self):
        self.assertEqual(score_run(self.case, {"turns": [TURN]}, world=None)["unjudged"], 2)


class SameFamilyReportTest(unittest.TestCase):
    def test_warns_when_judge_and_subject_share_a_family(self):
        cases = load_suite({"suite": "s", "cases": [{"id": "a", "say": "x"}]})
        records = {"a": [{"turns": [{"tokens": 1}], "result": {"outcome": "pass", "should": [0, 0]}}] * 3}
        qwen = summarize("s", {"model": "qwen3.8-plus", "judge": "qwen3.8-max"}, cases, records)
        self.assertIn("同属一个模型家族", render(compare(qwen, None)))
        other = summarize("s", {"model": "qwen3.8-plus", "judge": "deepseek-v4-pro"}, cases, records)
        self.assertNotIn("同属一个模型家族", render(compare(other, None)))


if __name__ == "__main__":
    unittest.main()
