"""The companion's MCP server answers every call as a result, and passes on only what the model gave.

CompanionToolTests stand in for the MCP SDK, which only the assistant's runtime
has. SdkPathTests run the server under the real SDK and skip without it, unless
ANAS_COMPANION_MCP_SDK=required, which makes the missing SDK a failure. make
ai-sdk-test (in make check-steps, so in CI) runs them so, under the SDK
Hermes's lock pins (ai/assistant-spike/companion-mcp-sdk.txt); the system test
with the runtime (ANAS_SYSTEM_TEST_ASSISTANT_RUNTIME) runs them under the
runtime itself, and refuses a skip. By hand, with the runtime's Python and its
packages (ai/assistant-spike/build-runtime.sh):

    PYTHONPATH=<runtime venv>/lib/python3.14/site-packages <runtime python> -m unittest tests.test_companion_mcp
"""

import importlib.util
import json
import os
import socket
import socketserver
import sys
import tempfile
import threading
import types
import unittest
from http.server import BaseHTTPRequestHandler
from pathlib import Path

SOURCE = Path(__file__).resolve().parents[1] / "assistant-spike" / "anas_companion_mcp.py"
SDK = bool(importlib.util.find_spec("mcp") and importlib.util.find_spec("mcp_types"))
SDK_REQUIRED = os.environ.get("ANAS_COMPANION_MCP_SDK") == "required"


class _Server:
    """Stands in for the MCP SDK's low-level server: the handlers stay plain functions."""

    def __init__(self, name, instructions="", on_list_tools=None, on_call_tool=None):
        self.name = name
        self.on_list_tools = on_list_tools
        self.on_call_tool = on_call_tool


class _Result:
    def __init__(self, **fields):
        self.__dict__.update(fields)


def _module(name, **attributes):
    module = types.ModuleType(name)
    module.__dict__.update(attributes)
    return module


def _exec(name):
    spec = importlib.util.spec_from_file_location(name, SOURCE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def load():
    """The server module with stand-ins for the MCP SDK and anyio."""
    stdio = _module("mcp.server.stdio", stdio_server=None)
    server = _module("mcp.server", Server=_Server, stdio=stdio)
    to_thread = _module("anyio.to_thread", run_sync=lambda function, *args: function(*args))
    stand_ins = {
        "mcp": _module("mcp", server=server),
        "mcp.server": server,
        "mcp.server.stdio": stdio,
        "mcp_types": _module("mcp_types", CallToolResult=_Result, ListToolsResult=_Result, TextContent=_Result, Tool=_Result),
        "anyio": _module("anyio", to_thread=to_thread, run=None),
        "anyio.to_thread": to_thread,
    }
    saved = {name: sys.modules.get(name) for name in stand_ins}
    sys.modules.update(stand_ins)
    try:
        return _exec("anas_companion_mcp")
    finally:
        for name, previous in saved.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


companion = load()


class _Service(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class _Companion:
    """A product service on a socket of its own, for the server module given."""

    def __init__(self, case, module):
        self.case = case
        self.module = module
        temporary = tempfile.TemporaryDirectory()
        case.addCleanup(temporary.cleanup)
        self.path = os.path.join(temporary.name, "companion.sock")
        case.addCleanup(setattr, module, "COMPANION", module.COMPANION)
        module.COMPANION = self.path
        self.received = []

    def serve(self, status, body):
        received = self.received

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers["Content-Length"])
                received.append((self.path, json.loads(self.rfile.read(length))))
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *_):
                pass

        self.listen(Handler)

    def listen(self, handler):
        service = _Service(self.path, handler)
        threading.Thread(target=service.serve_forever, daemon=True).start()
        self.case.addCleanup(service.server_close)
        self.case.addCleanup(service.shutdown)


class CompanionToolTests(unittest.TestCase):
    def setUp(self):
        self.service = _Companion(self, companion)
        self.path = self.service.path
        self.received = self.service.received

    def serve(self, status, body):
        self.service.serve(status, body)

    def assertAnswered(self, result, status):
        self.assertIs(result.is_error, False)
        self.assertEqual(result.structured_content["status"], status)
        self.assertEqual(len(result.content), 1)
        self.assertTrue(result.content[0].text)

    def test_without_the_socket_no_desktop_is_in_use(self):
        result = companion.call("broadcast", {"text": "你好"})
        self.assertAnswered(result, "no_desktop")
        self.assertIn("没有在用的桌面", result.content[0].text)

    def test_the_mount_point_alone_is_no_desktop_either(self):
        # A scheduled task's sandbox sees the empty file the socket is bound over elsewhere.
        Path(self.path).touch()
        self.assertAnswered(companion.call("wake", {}), "no_desktop")

    def test_a_refusal_of_the_product_service_is_no_desktop(self):
        self.serve(403, {"error": {"code": "authentication_required"}})
        self.assertAnswered(companion.call("sleep", None), "no_desktop")

    def test_a_call_too_long_to_read_is_a_bad_request(self):
        # The product service reads at most 8 KiB; the model hears what is too long.
        self.serve(413, {"error": {"code": "request_too_large"}})
        result = companion.call("broadcast", {"text": "长" * 4000})
        self.assertAnswered(result, "rejected")
        self.assertEqual(result.structured_content["reason"], "bad-request")
        self.assertIn("200 字", result.content[0].text)

    def test_the_service_answer_is_the_result(self):
        self.serve(200, {"status": "rejected", "reason": "sleeping", "message": "睡着了"})
        result = companion.call("anim", {"name": "wave", "facing": "left"})
        self.assertAnswered(result, "rejected")
        self.assertEqual(result.structured_content["reason"], "sleeping")
        self.assertIn("sleeping", result.content[0].text)
        self.assertIn("睡着了", result.content[0].text)
        self.assertEqual(self.received, [("/call", {"tool": "anim", "args": {"name": "wave", "facing": "left"}})])

    def test_only_the_given_arguments_are_passed_on(self):
        self.serve(200, {"status": "ok", "x": 0.5, "y": 1})
        result = companion.call("position", {"x": 0.5, "y": 1.5, "mode": "line", "duration": 300, "lock": None,
                                             "url": "http://example.com"})
        self.assertAnswered(result, "ok")
        self.assertEqual(self.received[0][1], {"tool": "position", "args": {"x": 0.5, "y": 1.5, "mode": "line", "duration": 300}})
        companion.call("lock", None)
        self.assertEqual(self.received[1][1], {"tool": "lock", "args": {}})

    def test_an_answer_without_a_status_is_no_desktop(self):
        self.serve(200, ["ok"])
        self.assertAnswered(companion.call("bubble_clear", {}), "no_desktop")

    def test_a_service_that_never_answers_is_a_timeout(self):
        listener = socket.socket(socket.AF_UNIX)
        self.addCleanup(listener.close)
        listener.bind(self.path)
        listener.listen()
        self.addCleanup(setattr, companion, "TIMEOUT", companion.TIMEOUT)
        companion.TIMEOUT = 0.2
        self.assertAnswered(companion.call("wake", {}), "timeout")

    def test_a_service_gone_once_the_call_was_sent_is_a_timeout(self):
        # The product service restarted with the call on its way: the window may
        # have acted, so the model must not hear that nothing happened.
        class Handler(socketserver.StreamRequestHandler):
            def handle(self):
                self.rfile.readline()

        self.service.listen(Handler)
        self.assertAnswered(companion.call("wake", {}), "timeout")

    def test_the_schema_is_only_what_the_model_is_shown(self):
        listed = {tool.name: tool.input_schema for tool in companion.tools()}
        self.assertEqual(set(listed), {"anim", "broadcast", "bubble_clear", "position", "throw", "sleep", "wake", "lock", "lock_release"})
        self.assertEqual(listed["position"]["required"], ["x", "y", "mode"])
        self.assertEqual(listed["position"]["properties"]["mode"], {"type": "string", "enum": ["instant", "spring", "line"]})
        self.assertEqual(listed["lock_release"]["required"], ["lockId"])
        for schema in listed.values():
            self.assertEqual(schema["type"], "object")
            for name, property in schema["properties"].items():
                self.assertIn("type", property, name)

    def test_an_argument_utf8_cannot_carry_is_a_bad_request(self):
        # A lone surrogate, as JSON's \ud800 decodes: the call never leaves.
        self.serve(200, {"status": "ok"})
        result = companion.call("broadcast", {"text": "\ud800"})
        self.assertAnswered(result, "rejected")
        self.assertEqual(result.structured_content["reason"], "bad-request")
        self.assertEqual(self.received, [])

    def test_wrong_and_missing_values_reach_the_service_as_given(self):
        self.serve(200, {"status": "rejected", "reason": "bad-request", "message": "mode：应为 instant、spring、line 之一"})
        result = companion.call("position", {"x": 0.5, "y": "0.5", "mode": "teleport", "duration": "soon"})
        self.assertAnswered(result, "rejected")
        self.assertEqual(self.received[0][1]["args"], {"x": 0.5, "y": "0.5", "mode": "teleport", "duration": "soon"})
        companion.call("anim", {"facing": "", "lock": ""})
        self.assertEqual(self.received[1][1]["args"], {"facing": "", "lock": ""})


@unittest.skipUnless(SDK or SDK_REQUIRED, "the MCP SDK is in the assistant's runtime and make ai-sdk-test only")
class SdkPathTests(unittest.TestCase):
    """The server under the real SDK, called as Hermes's MCP client calls it."""

    def setUp(self):
        if not SDK:
            self.fail("ANAS_COMPANION_MCP_SDK=required, but the MCP SDK cannot be imported")
        self.module = _exec("anas_companion_mcp_sdk")
        self.service = _Companion(self, self.module)

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

    def test_every_call_reaches_the_product_service(self):
        self.service.serve(200, {"status": "rejected", "reason": "bad-request", "message": "缺少参数"})
        given = [
            # Missing required arguments: no SDK error, which Hermes's circuit breaker counts.
            ("anim", {}), ("lock_release", {}), ("position", {"x": 1}), ("throw", {"power": 1}),
            # Strings that look like JSON stay strings.
            ("broadcast", {"text": "42"}), ("broadcast", {"text": "true"}), ("broadcast", {"text": "[1,2]"}),
            ("broadcast", {"text": "null"}), ("broadcast", {"text": '{"a":1}'}),
            # Keys the tool does not take are dropped, as before.
            ("anim", {"name": "wave", "url": "http://example.com"}),
        ]
        listed, results = self.calls(*given)
        required = {tool.name: tool.input_schema.get("required") for tool in listed.tools}
        self.assertEqual(required["anim"], ["name"])
        self.assertEqual(required["position"], ["x", "y", "mode"])
        for (tool, arguments), result in zip(given, results, strict=True):
            self.assertIs(result.is_error, False, f"{tool} {arguments}")
            self.assertEqual(result.structured_content["status"], "rejected", f"{tool} {arguments}")
        sent = [call["args"] for _, call in self.service.received]
        self.assertEqual(sent, [{}, {}, {"x": 1}, {"power": 1}, {"text": "42"}, {"text": "true"}, {"text": "[1,2]"},
                                {"text": "null"}, {"text": '{"a":1}'}, {"name": "wave"}])

    def test_an_argument_utf8_cannot_carry_is_a_result(self):
        # Hermes's client sends JSON, in which "\ud800" is a lone surrogate.
        self.service.serve(200, {"status": "ok"})
        _, results = self.calls(("broadcast", {"text": "\ud800"}))
        self.assertIs(results[0].is_error, False)
        self.assertEqual(results[0].structured_content["reason"], "bad-request")
        self.assertEqual(self.service.received, [])


if __name__ == "__main__":
    unittest.main()
