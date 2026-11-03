"""The same gateway routes Qwen-compatible cloud prefixes and local models."""

import http.client
import importlib.util
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "assistant-spike" / "gateway.py"
SPEC = importlib.util.spec_from_file_location("model_gateway", SOURCE)
gateway = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gateway)


class BaseURLTests(unittest.TestCase):
    def test_connect_proxy_keeps_credentials_out_of_url_and_connect_headers(self):
        proxy = gateway.https_proxy("http://127.0.0.1:7897")
        with patch.object(http.client, "HTTPSConnection") as connection:
            gateway.model_connection(gateway.upstream_base("https://models.example:8443/v1"), proxy)
            connection.assert_called_once_with("127.0.0.1", 7897, timeout=300)
            connection.return_value.set_tunnel.assert_called_once_with("models.example", 8443)
        for value in ["https://proxy.example", "http://user:secret@proxy.example", "http://proxy.example/path", "http://proxy.example?key=secret", "http://proxy.example#key", "http://proxy.example:99999", "http://proxy.example\n"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                gateway.https_proxy(value)

    def test_local_models_bypass_the_explicit_cloud_proxy(self):
        proxy = gateway.https_proxy("http://127.0.0.1:7897")
        for url in ["http://127.0.0.1:8000/v1", "https://localhost/v1"]:
            base = gateway.upstream_base(url)
            factory = "HTTPConnection" if base.scheme == "http" else "HTTPSConnection"
            with self.subTest(url=url), patch.object(http.client, factory) as connection:
                gateway.model_connection(base, proxy)
                connection.assert_called_once_with(base.netloc, timeout=300)
                connection.return_value.set_tunnel.assert_not_called()

    def test_existing_deepseek_host_is_unchanged(self):
        base = gateway.upstream_base("api.deepseek.com")
        self.assertEqual((base.scheme, base.netloc), ("https", "api.deepseek.com"))
        self.assertEqual(gateway.upstream_path(base, "/v1/chat/completions"), "/v1/chat/completions")

    def test_cloud_prefix_and_local_paths_share_the_same_contract(self):
        cases = [
            ("https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", "/compatible-mode/v1/chat/completions"),
            ("https://dashscope.aliyuncs.com/compatible-mode/v1/", "/compatible-mode/v1/chat/completions"),
            ("http://127.0.0.1:8000/v1", "/v1/chat/completions"),
            ("http://[::1]:8000/v1", "/v1/chat/completions"),
        ]
        for url, expected in cases:
            with self.subTest(url=url):
                self.assertEqual(gateway.upstream_path(gateway.upstream_base(url), "/v1/chat/completions"), expected)
        base = gateway.upstream_base("https://example.test/compatible-mode/v1")
        self.assertEqual(gateway.upstream_path(base, "/v1/models?limit=2"), "/compatible-mode/v1/models?limit=2")

    def test_rejects_credential_urls_and_public_cleartext(self):
        for url in ["", " http://localhost/v1", "http://example.test/v1", "https://key@example.test/v1", "https://example.test/v1?key=test", "https://example.test/v1#fragment", "ftp://example.test/v1", "https://example.test:99999/v1", "https://example.test/evil\npath"]:
            with self.subTest(url=url), self.assertRaises(ValueError):
                gateway.upstream_base(url)

    def test_request_cannot_replace_the_configured_origin(self):
        base = gateway.upstream_base("https://example.test/compatible-mode/v1")
        for path in ["https://other.test/v1/chat/completions", "//other.test/v1/models", "/v10/models", "/api/config"]:
            with self.subTest(path=path), self.assertRaises(ValueError):
                gateway.upstream_path(base, path)

    def test_configuration_check_needs_neither_a_key_nor_a_model(self):
        for url, success in [("https://workspace.example/compatible-mode/v1", True), ("http://127.0.0.1:8000/v1", True), ("https://secret@example.test/v1", False)]:
            result = subprocess.run([sys.executable, str(SOURCE), "--check-upstream", url], capture_output=True, text=True, timeout=3, check=False)
            self.assertEqual(result.returncode == 0, success)
            self.assertNotIn("secret@example", result.stderr + result.stdout)

    def test_no_auth_mode_cannot_target_cloud(self):
        with tempfile.TemporaryDirectory(prefix="anas-gateway-", dir="/tmp") as folder:
            root = Path(folder)
            result = subprocess.run([sys.executable, str(SOURCE), str(root / "unused.sock"), str(root / "unused.jsonl"), "https://example.test/v1", str(root / "must-not-read.key")], env={**os.environ, "GATEWAY_LOCAL_NO_AUTH": "1"}, capture_output=True, text=True, timeout=3, check=False)
            self.assertEqual(list(root.iterdir()), [])
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no-auth model mode requires a loopback", result.stderr)


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=3)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


class WireTests(unittest.TestCase):
    no_auth = False

    def setUp(self):
        # A short path also fits macOS's Unix socket pathname limit.
        self.temporary = tempfile.TemporaryDirectory(prefix="anas-gateway-", dir="/tmp")
        self.addCleanup(self.temporary.cleanup)
        self.folder = Path(self.temporary.name)
        self.requests = []
        self.agents = []
        records = self.requests
        agents = self.agents

        class Model(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                records.append((self.path, self.headers.get("Authorization"), body))
                agents.append(self.headers.get("User-Agent"))
                if body.get("stream"):
                    data = b'data: {"choices":[],"usage":{"total_tokens":3}}\n\ndata: [DONE]\n\n'
                    content_type = "text/event-stream"
                else:
                    data = json.dumps({"choices": [{"message": {"role": "assistant", "tool_calls": [{"id": "fixture-1", "type": "function", "function": {"name": "set_reminder", "arguments": '{"text":"synthetic"}'}}]}}], "usage": {"total_tokens": 5}}).encode()
                    content_type = "application/json"
                self.send_response(200)
                self.send_header("Content-Type", content_type)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.model = ThreadingHTTPServer(("127.0.0.1", 0), Model)
        self.thread = threading.Thread(target=self.model.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_model)
        self.key = self.folder / "fixture.key"
        self.key.write_text("fixture-not-a-real-key\n")
        self.key.chmod(0o600)
        self.sock = self.folder / "model.sock"
        self.log = self.folder / "requests.jsonl"
        self.process = subprocess.Popen(
            [sys.executable, str(SOURCE), str(self.sock), str(self.log), f"http://127.0.0.1:{self.model.server_port}/compatible-mode/v1", str(self.folder / "must-not-read.key") if self.no_auth else str(self.key)],
            env={**os.environ, "GATEWAY_LOG_MESSAGES": "0", "GATEWAY_LOCAL_NO_AUTH": "1" if self.no_auth else "0"},
            stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        )
        self.addCleanup(self.stop_process)
        deadline = time.monotonic() + 3
        while not self.sock.exists() and time.monotonic() < deadline:
            if self.process.poll() is not None:
                self.fail("gateway exited before creating its test socket")
            time.sleep(0.01)
        self.assertTrue(self.sock.exists(), "gateway did not become ready")

    def stop_model(self):
        self.model.shutdown()
        self.model.server_close()
        self.thread.join(timeout=3)

    def stop_process(self):
        self.process.terminate()
        self.process.wait(timeout=3)
        self.process.stderr.close()

    def call(self, body, path="/v1/chat/completions"):
        connection = UnixHTTPConnection(self.sock)
        self.addCleanup(connection.close)
        connection.request("POST", path, json.dumps(body), {"Content-Type": "application/json", "Authorization": "Bearer sandbox-dummy", "User-Agent": "fixture-sdk/1.0"})
        response = connection.getresponse()
        return response.status, response.read()

    def test_tool_calls_and_model_are_preserved_and_real_key_stays_out_of_logs(self):
        payload = {"model": "qwen-fixture", "messages": [{"role": "user", "content": "synthetic-private-message"}], "tools": [{"type": "function", "function": {"name": "set_reminder", "parameters": {"type": "object"}}}]}
        status, data = self.call(payload)
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["choices"][0]["message"]["tool_calls"][0]["function"]["name"], "set_reminder")
        self.assertEqual(self.requests, [("/compatible-mode/v1/chat/completions", None if self.no_auth else "Bearer fixture-not-a-real-key", payload)])
        # The response may arrive just before the request log is written.
        deadline = time.monotonic() + 1
        while not self.log.exists() and time.monotonic() < deadline:
            time.sleep(0.01)
        logged = self.log.read_text()
        self.assertNotIn("synthetic-private-message", logged)
        self.assertNotIn("fixture-not-a-real-key", logged)
        self.assertEqual(json.loads(logged)["messages"], 1)

    def test_stream_bytes_and_usage_are_forwarded(self):
        status, data = self.call({"model": "local-fixture", "stream": True})
        self.assertEqual(status, 200)
        self.assertIn(b"data: [DONE]", data)
        self.assertEqual(self.requests[0][0], "/compatible-mode/v1/chat/completions")
        self.assertEqual(self.agents, ["fixture-sdk/1.0"])

    def test_unknown_path_does_not_reach_the_model(self):
        status, _data = self.call({}, "/api/config")
        self.assertEqual(status, 400)
        self.assertEqual(self.requests, [])


class LocalNoAuthTests(WireTests):
    """The same payloads work locally, without loading a cloud credential."""

    no_auth = True


if __name__ == "__main__":
    unittest.main()
