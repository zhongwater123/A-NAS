"""The companion's MCP server answers every call as a result, and passes on only what the model gave."""

import importlib.util
import inspect
import json
import os
import socket
import socketserver
import sys
import tempfile
import threading
import types
import typing
import unittest
from http.server import BaseHTTPRequestHandler
from pathlib import Path

SOURCE = Path(__file__).resolve().parents[1] / "assistant-spike" / "anas_companion_mcp.py"


class _Server:
    """Stands in for the MCP SDK's server: the tools stay plain functions."""

    def __init__(self, name, instructions=""):
        self.name = name

    def tool(self, **_):
        return lambda function: function


class _Result:
    def __init__(self, **fields):
        self.__dict__.update(fields)


class _WithJsonSchema:
    """Stands in for pydantic's WithJsonSchema, which carries the schema the same way."""

    def __init__(self, json_schema):
        self.json_schema = json_schema


def load():
    """The server module with stand-ins for the MCP SDK, which only the assistant's runtime has."""
    server = types.ModuleType("mcp.server")
    server.MCPServer = _Server
    mcp = types.ModuleType("mcp")
    mcp.server = server
    mcp_types = types.ModuleType("mcp_types")
    mcp_types.CallToolResult = _Result
    mcp_types.TextContent = _Result
    pydantic = types.ModuleType("pydantic")
    pydantic.WithJsonSchema = _WithJsonSchema
    saved = {name: sys.modules.get(name) for name in ("mcp", "mcp.server", "mcp_types", "pydantic")}
    sys.modules.update({"mcp": mcp, "mcp.server": server, "mcp_types": mcp_types, "pydantic": pydantic})
    try:
        spec = importlib.util.spec_from_file_location("anas_companion_mcp", SOURCE)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module
    finally:
        for name, previous in saved.items():
            if previous is None:
                sys.modules.pop(name, None)
            else:
                sys.modules[name] = previous


companion = load()


class _Service(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


class CompanionToolTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.path = os.path.join(self.temporary.name, "companion.sock")
        self.addCleanup(setattr, companion, "COMPANION", companion.COMPANION)
        companion.COMPANION = self.path
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

        service = _Service(self.path, Handler)
        threading.Thread(target=service.serve_forever, daemon=True).start()
        self.addCleanup(service.server_close)
        self.addCleanup(service.shutdown)

    def assertAnswered(self, result, status):
        self.assertIs(result.is_error, False)
        self.assertEqual(result.structured_content["status"], status)
        self.assertEqual(len(result.content), 1)
        self.assertTrue(result.content[0].text)

    def test_without_the_socket_no_desktop_is_in_use(self):
        result = companion.broadcast("你好")
        self.assertAnswered(result, "no_desktop")
        self.assertIn("没有在用的桌面", result.content[0].text)

    def test_the_mount_point_alone_is_no_desktop_either(self):
        # A scheduled task's sandbox sees the empty file the socket is bound over elsewhere.
        Path(self.path).touch()
        self.assertAnswered(companion.wake(), "no_desktop")

    def test_a_refusal_of_the_product_service_is_no_desktop(self):
        self.serve(403, {"error": {"code": "authentication_required"}})
        self.assertAnswered(companion.sleep(), "no_desktop")

    def test_the_service_answer_is_the_result(self):
        self.serve(200, {"status": "rejected", "reason": "sleeping", "message": "睡着了"})
        result = companion.anim("wave", facing="left")
        self.assertAnswered(result, "rejected")
        self.assertEqual(result.structured_content["reason"], "sleeping")
        self.assertIn("sleeping", result.content[0].text)
        self.assertEqual(self.received, [("/call", {"tool": "anim", "args": {"name": "wave", "facing": "left"}})])

    def test_only_the_given_arguments_are_passed_on(self):
        self.serve(200, {"status": "ok", "x": 0.5, "y": 1})
        result = companion.position(0.5, 1.5, "line", duration=300)
        self.assertAnswered(result, "ok")
        self.assertEqual(self.received[0][1], {"tool": "position", "args": {"x": 0.5, "y": 1.5, "mode": "line", "duration": 300}})
        companion.lock()
        self.assertEqual(self.received[1][1], {"tool": "lock", "args": {}})

    def test_an_answer_without_a_status_is_no_desktop(self):
        self.serve(200, ["ok"])
        self.assertAnswered(companion.bubble_clear(), "no_desktop")

    def test_a_service_that_never_answers_is_a_timeout(self):
        listener = socket.socket(socket.AF_UNIX)
        self.addCleanup(listener.close)
        listener.bind(self.path)
        listener.listen()
        self.addCleanup(setattr, companion, "TIMEOUT", companion.TIMEOUT)
        companion.TIMEOUT = 0.2
        self.assertAnswered(companion.wake(), "timeout")

    def test_the_sdk_checks_no_argument(self):
        # The SDK's own validation would answer a wrong value as an error, which
        # Hermes's circuit breaker counts; the product service answers it instead.
        for tool in ("anim", "broadcast", "bubble_clear", "position", "throw", "sleep", "wake", "lock", "lock_release"):
            function = getattr(companion, tool)
            hints = typing.get_type_hints(function, include_extras=True)
            for name in inspect.signature(function).parameters:
                self.assertIs(typing.get_origin(hints[name]), typing.Annotated, f"{tool}.{name}")
                kind, schema = typing.get_args(hints[name])
                self.assertIs(kind, typing.Any, f"{tool}.{name}")
                self.assertIn("type", schema.json_schema, f"{tool}.{name}")
        mode = typing.get_args(typing.get_type_hints(companion.position, include_extras=True)["mode"])[1]
        self.assertEqual(mode.json_schema, {"type": "string", "enum": ["instant", "spring", "line"]})

    def test_wrong_values_reach_the_service_as_given(self):
        self.serve(200, {"status": "rejected", "reason": "bad-request", "message": "mode：应为 instant、spring、line 之一"})
        result = companion.position(0.5, "0.5", "teleport", duration="soon")
        self.assertAnswered(result, "rejected")
        self.assertEqual(self.received[0][1]["args"], {"x": 0.5, "y": "0.5", "mode": "teleport", "duration": "soon"})
        companion.anim("wave", facing="", lock="")
        self.assertEqual(self.received[1][1]["args"], {"name": "wave", "facing": "", "lock": ""})


if __name__ == "__main__":
    unittest.main()
