"""The montage center's MCP server tells the model what each answer means, and passes on its answers as answers.

The planning round on the Experimental NAS (2026-11-19) showed how the seams between the model, this server and
the montage center fail where each side's own tests pass. These run the server with stand-ins for the MCP SDK
(tests.test_companion_mcp.load) against a montage center on a socket of its own.
"""

import unittest
from pathlib import Path

from tests import test_companion_mcp as companion_tests

SOURCE = Path(__file__).resolve().parents[1] / "assistant" / "anas_montage_mcp.py"
PROMPT = Path(__file__).resolve().parents[1] / "anas_montage" / "prompts" / "plan-v2.md"


def montage_server():
    """The server module under the companion tests' stand-ins, with the ToolAnnotations it also imports."""
    original = companion_tests._module

    def with_annotations(name, **attributes):
        if name == "mcp_types":
            attributes.setdefault("ToolAnnotations", companion_tests._Result)
        return original(name, **attributes)

    companion_tests._module = with_annotations
    try:
        return companion_tests.load("anas_montage_mcp", SOURCE)
    finally:
        companion_tests._module = original


class MontageToolTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.module = montage_server()

    def center(self, status, body):
        center = companion_tests._Companion(self, self.module, attribute="SOCKET")
        center.serve(status, body)
        return center

    @staticmethod
    def said(result):
        return result.content[0].text

    def test_an_answer_written_as_a_json_string_reaches_the_montage_center_as_an_object(self):
        center = self.center(200, {"status": "accepted"})
        self.module.call("montage_submit_outline", {"jobId": "job:X", "attemptToken": "ab" * 16, "answer": '{"design": "海"}'})
        self.assertEqual(center.received,
                         [("/v1/tools/outline", {"jobId": "job:X", "attemptToken": "ab" * 16, "answer": {"design": "海"}})])

    def test_each_acceptance_says_the_next_step_and_none_reads_as_another(self):
        self.center(200, {"status": "accepted"})
        outline = self.said(self.module.call("montage_submit_outline", {"jobId": "job:X", "attemptToken": "t", "answer": {}}))
        shots = self.said(self.module.call("montage_submit_shots", {"jobId": "job:X", "attemptToken": "t", "answer": {}}))
        self.assertIn("大纲已收下", outline)
        self.assertIn("全部方案都已收下", shots)
        # The model stops at 全部方案都已收下 and goes on at 还有下一版 (plan-v2.md): neither may be read in the other.
        next_plan = self.module.SAID["next_plan"]
        self.assertIn("还有下一版", next_plan)
        self.assertNotIn("已收下", next_plan)
        for said in (outline, shots):
            self.assertNotIn("还有下一版", said)
        prompt = PROMPT.read_text(encoding="utf-8")
        for word in ("大纲已收下", "还有下一版", "全部方案都已收下", "mcp__montage__"):
            self.assertIn(word, prompt)

    def test_a_call_the_montage_center_refuses_as_a_request_is_rejected_with_what_to_fix(self):
        self.center(400, {"error": {"code": "validation_failed", "message": "the request is not valid"}})
        said = self.said(self.module.call("montage_brief", {"jobId": 7, "attemptToken": "t"}))
        self.assertIn("不合格", said)
        self.assertIn("jobId", said)
        self.assertNotIn("连不上", said)


if __name__ == "__main__":
    unittest.main()
