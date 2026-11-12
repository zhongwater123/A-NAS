"""The desktop's MCP server passes each step to the product service and says what came back.

DesktopToolTests stand in for the MCP SDK, which only the assistant's runtime has.
SdkPathTests run the server under the real SDK and skip without it; the system test with
the runtime runs them and refuses a skip (see test_companion_mcp.py for how to by hand).
"""

import importlib.util
import unittest

from tests.test_companion_mcp import SPIKE, _Companion, _exec, load

SOURCE = SPIKE / "anas_desktop_mcp.py"
PNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

desktop = load("anas_desktop_mcp", SOURCE)


class DesktopToolTests(unittest.TestCase):
    def setUp(self):
        self.service = _Companion(self, desktop, "DESKTOP")
        self.received = self.service.received

    def text(self, result, status):
        self.assertIs(result.is_error, False)
        if status == "ok":
            # A step that was taken returns only what it returned.
            self.assertIsNone(getattr(result, "structured_content", None))
        else:
            self.assertEqual(result.structured_content["status"], status)
        return result.content[0].text

    def test_without_the_socket_no_desktop_is_in_use(self):
        said = self.text(desktop.call("snapshot", {}), "no_desktop")
        self.assertIn("没有在用的桌面", said)

    def test_a_snapshot_is_the_page_as_text(self):
        self.service.serve(200, {"status": "ok", "result": {"snapshot": '- window "相册"\n  - button "上传" [ref=e1]'}})
        said = self.text(desktop.call("snapshot", {"full": True, "url": "https://example.com", "depth": None}), "ok")
        self.assertEqual(said, '- window "相册"\n  - button "上传" [ref=e1]')
        # The steps have a route of their own, and only the tool's own arguments are passed on.
        self.assertEqual(self.received, [("/desktop/call", {"tool": "snapshot", "args": {"full": True}})])

    def test_a_step_returns_what_the_window_did(self):
        self.service.serve(200, {"status": "ok", "result": {"clicked": "@e2", "element": 'button "相册"'}})
        said = self.text(desktop.call("click", {"ref": "e2"}), "ok")
        self.assertIn("相册", said)
        self.assertEqual(self.received[0][1], {"tool": "click", "args": {"ref": "e2"}})

    def test_a_picture_comes_back_as_an_image_and_never_as_text(self):
        self.service.serve(200, {"status": "ok", "result": {
            "mime_type": "image/png", "data": PNG, "width": 1, "height": 1,
            "annotations": [{"ref": "e1", "name": "上传"}, {"ref": "e2", "name": "相册"}, {"ref": "e3"}, {"name": "无编号"}, "e9"]}})
        result = desktop.call("look", {"annotate": True})
        said = self.text(result, "ok")
        self.assertIn("vision_analyze", said)
        # A control without a name is its ref alone, and what is no labelled control is left out.
        self.assertIn("e1 上传；e2 相册；e3。", said)
        self.assertNotIn("None", said)
        self.assertEqual(len(result.content), 2)
        self.assertEqual((result.content[1].type, result.content[1].data, result.content[1].mime_type), ("image", PNG, "image/png"))
        self.assertNotIn(PNG, said)

    def test_a_picture_that_is_not_one_is_not_passed_on(self):
        self.service.serve(200, {"status": "ok", "result": {"mime_type": "text/html", "data": "<script>"}})
        result = desktop.call("look", {})
        self.assertEqual(len(result.content), 1)
        self.assertIn("没有取到", self.text(result, "ok"))

    def test_a_step_the_window_did_not_take_says_why(self):
        self.service.serve(200, {"status": "failed", "message": "成员没有允许这一步，NAS 上什么都没有改"})
        said = self.text(desktop.call("press", {"key": "Enter"}), "failed")
        self.assertIn("NAS 上什么都没有改", said)
        self.assertTrue(said.endswith("。"))

    def test_a_call_too_long_to_read_is_a_bad_request(self):
        self.service.serve(413, {"error": {"code": "request_too_large"}})
        said = self.text(desktop.call("type", {"ref": "e1", "text": "长" * 4000}), "rejected")
        self.assertIn("4000", said)

    def test_the_tools_never_leave_the_desktop(self):
        listed = {tool.name: tool.input_schema for tool in desktop.tools()}
        self.assertEqual(set(listed), {"snapshot", "click", "type", "press", "scroll", "look"})
        self.assertEqual(listed["type"]["required"], ["ref", "text"])
        self.assertEqual(listed["scroll"]["properties"]["direction"], {"type": "string", "enum": ["up", "down"]})
        for schema in listed.values():
            self.assertNotIn("url", schema["properties"])


@unittest.skipUnless(importlib.util.find_spec("mcp") and importlib.util.find_spec("mcp_types"), "the MCP SDK is in the assistant's runtime only")
class SdkPathTests(unittest.TestCase):
    """The server under the real SDK, called as Hermes's MCP client calls it."""

    def setUp(self):
        self.module = _exec("anas_desktop_mcp_sdk", SOURCE)
        self.service = _Companion(self, self.module, "DESKTOP")

    def calls(self, *calls):
        import anyio
        from mcp.client import Client

        results = []

        async def run():
            async with Client(self.module.server) as client:
                results.append(await client.list_tools())
                for tool, arguments in calls:
                    results.append(await client.call_tool(tool, arguments))

        anyio.run(run)
        return results[0], results[1:]

    def test_a_picture_reaches_the_client_as_an_image(self):
        self.service.serve(200, {"status": "ok", "result": {"mime_type": "image/png", "data": PNG, "width": 1, "height": 1}})
        listed, (result,) = self.calls(("look", {}))
        self.assertEqual({tool.name for tool in listed.tools}, {"snapshot", "click", "type", "press", "scroll", "look"})
        self.assertIs(result.is_error, False)
        kinds = [block.type for block in result.content]
        self.assertEqual(kinds, ["text", "image"])
        self.assertEqual(result.content[1].data, PNG)

    def test_missing_arguments_reach_the_product_service(self):
        # No SDK error for a missing argument, which Hermes's circuit breaker would count.
        self.service.serve(200, {"status": "rejected", "reason": "bad-request", "message": "缺少参数 ref"})
        _, results = self.calls(("click", {}), ("type", {"ref": "e1"}), ("scroll", {}))
        for result in results:
            self.assertIs(result.is_error, False)
            self.assertEqual(result.structured_content["status"], "rejected")
        self.assertEqual([call["args"] for _, call in self.service.received], [{}, {"ref": "e1"}, {}])


if __name__ == "__main__":
    unittest.main()
