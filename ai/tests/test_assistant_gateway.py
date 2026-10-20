"""The assistant's model gateway (ai/assistant-spike/gateway.py) with a plain-HTTP upstream.

A fake OpenAI-compatible server on 127.0.0.1 stands in for NVIDIA PAIR's Ollama
proxy on the NAS: what reaches it, and what the assistant gets when PAIR is not
running (standard library only).
"""

import http.client
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

GATEWAY = os.path.join(os.path.dirname(__file__), "..", "assistant-spike", "gateway.py")


class Upstream:
    """An OpenAI-compatible server that records the requests it gets."""

    def __init__(self):
        self.requests = []
        seen = self.requests

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                seen.append({"method": "GET", "path": self.path, "authorization": self.headers.get("Authorization")})
                self.reply("application/json", json.dumps({"data": [{"id": "qwen3.5:9b"}]}).encode())

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                seen.append({"method": "POST", "path": self.path, "authorization": self.headers.get("Authorization"), "body": body})
                if body.get("stream"):
                    events = [{"choices": [{"delta": {"content": "你好"}}]},
                              {"choices": [], "usage": {"prompt_tokens": 7, "completion_tokens": 2}}]
                    data = "".join(f"data: {json.dumps(e, ensure_ascii=False)}\n\n" for e in events) + "data: [DONE]\n\n"
                    self.reply("text/event-stream", data.encode())
                else:
                    answer = {"choices": [{"message": {"role": "assistant", "content": "ok"}}],
                              "usage": {"prompt_tokens": 5, "completion_tokens": 1}}
                    self.reply("application/json", json.dumps(answer).encode())

            def reply(self, kind, data):
                self.send_response(200)
                self.send_header("Content-Type", kind)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.url = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

    def close(self):
        self.server.shutdown()
        self.server.server_close()


def closed_port():
    """A local port nothing listens on: the connection is refused, as when PAIR is stopped."""
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class UnixConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=10)
        self.unix_path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.unix_path)


class GatewayTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.sock = os.path.join(self.dir, "model.sock")
        self.log = os.path.join(self.dir, "requests.jsonl")
        self.gateway = None

    def tearDown(self):
        if self.gateway:
            self.gateway.terminate()
            self.gateway.wait(5)

    def start(self, upstream, key="-"):
        env = dict(os.environ, GATEWAY_LOG_MESSAGES="0")
        self.gateway = subprocess.Popen([sys.executable, GATEWAY, self.sock, self.log, upstream, key], env=env)
        for _ in range(100):
            if os.path.exists(self.sock):
                return
            self.assertIsNone(self.gateway.poll(), "the gateway exited")
            time.sleep(0.05)
        self.fail("the gateway did not listen")

    def call(self, method, path, body=None):
        conn = UnixConnection(self.sock)
        conn.request(method, path, body=json.dumps(body).encode() if body is not None else None,
                     headers={"Content-Type": "application/json"})
        resp = conn.getresponse()
        data = resp.read()
        conn.close()
        return resp.status, resp.getheader("Content-Type"), data

    def logged(self):
        """The log's records; the gateway writes one after it has answered."""
        for _ in range(100):
            if os.path.exists(self.log):
                with open(self.log, encoding="utf-8") as f:
                    records = [json.loads(line) for line in f if line.endswith("\n")]
                if records:
                    return records
            time.sleep(0.02)
        return []

    def test_plain_http_upstream_gets_the_request_without_a_key(self):
        node = Upstream()
        self.addCleanup(node.close)
        self.start(node.url)
        status, _, data = self.call("POST", "/v1/chat/completions",
                                    {"model": "qwen3.5:9b", "messages": [{"role": "user", "content": "hi"}]})
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["choices"][0]["message"]["content"], "ok")
        self.assertEqual(node.requests[0]["path"], "/v1/chat/completions")
        self.assertIsNone(node.requests[0]["authorization"])
        record = self.logged()[0]
        self.assertEqual(record["messages"], 1)
        self.assertEqual(record["usage"], {"prompt_tokens": 5, "completion_tokens": 1})

    def test_streaming_is_relayed_and_usage_logged(self):
        node = Upstream()
        self.addCleanup(node.close)
        self.start(node.url)
        status, kind, data = self.call("POST", "/v1/chat/completions",
                                       {"model": "qwen3.5:9b", "stream": True, "messages": []})
        self.assertEqual(status, 200)
        self.assertIn("text/event-stream", kind)
        self.assertIn("你好", data.decode())
        self.assertTrue(data.decode().rstrip().endswith("data: [DONE]"))
        self.assertEqual(self.logged()[0]["usage"], {"prompt_tokens": 7, "completion_tokens": 2})

    def test_model_list_is_forwarded(self):
        node = Upstream()
        self.addCleanup(node.close)
        self.start(node.url)
        status, _, data = self.call("GET", "/v1/models")
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(data)["data"][0]["id"], "qwen3.5:9b")

    def test_pair_not_running_is_an_error_the_assistant_can_show(self):
        self.start(f"http://127.0.0.1:{closed_port()}")
        status, kind, data = self.call("POST", "/v1/chat/completions", {"model": "m", "messages": []})
        self.assertEqual(status, 502)
        self.assertEqual(kind, "application/json")
        self.assertEqual(json.loads(data)["error"]["type"], "upstream_unavailable")
        self.assertEqual(self.logged()[0]["status"], 502)

    def test_ollama_probe_is_not_forwarded(self):
        node = Upstream()
        self.addCleanup(node.close)
        self.start(node.url)
        status, _, _ = self.call("POST", "/api/show", {"model": "m"})
        self.assertEqual(status, 404)
        self.assertEqual(node.requests, [])

    def test_https_upstream_without_a_key_does_not_start(self):
        result = subprocess.run([sys.executable, GATEWAY, self.sock, self.log, "api.example.com", "-"],
                                capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("needs the key", result.stderr)

    def test_a_url_with_a_path_is_refused(self):
        result = subprocess.run([sys.executable, GATEWAY, self.sock, self.log, "http://pc.lan:11434/v1", "-"],
                                capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not a host", result.stderr)


if __name__ == "__main__":
    unittest.main()
