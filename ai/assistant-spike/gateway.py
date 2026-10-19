"""Model gateway for the spike: the stand-in for the product service's gateway.

Listens on the Unix socket bound into the sandbox, adds the provider key that
only this process can read, forwards to an OpenAI-compatible upstream and logs
every request (tools offered, messages) and the token usage it saw. With
GATEWAY_LOG_MESSAGES=0 (the trial on the Experimental NAS, where the requests
carry members' files) it logs how many messages there were instead of them.
"""
import http.client
import json
import os
import socketserver
import sys
import time
from http.server import BaseHTTPRequestHandler

SOCK, LOG, UPSTREAM_HOST, KEY_FILE = sys.argv[1:5]
KEY = open(KEY_FILE, encoding="utf-8").read().strip()
LOG_MESSAGES = os.environ.get("GATEWAY_LOG_MESSAGES", "1") != "0"


def log(record):
    with open(LOG, "a", encoding="utf-8") as out:
        out.write(json.dumps(record, ensure_ascii=False) + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def address_string(self):
        return "uds"

    def forward(self, method, body=b""):
        if self.path.startswith("/api/show"):
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        upstream = http.client.HTTPSConnection(UPSTREAM_HOST, timeout=300)
        headers = {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json", "Accept": self.headers.get("Accept", "*/*")}
        upstream.request(method, self.path, body=body or None, headers=headers)
        resp = upstream.getresponse()
        self.send_response(resp.status)
        self.send_header("Content-Type", resp.getheader("Content-Type", "application/json"))
        self.send_header("Connection", "close")
        streaming = "text/event-stream" in (resp.getheader("Content-Type") or "")
        usage, tail = None, b""
        if not streaming:
            data = resp.read()
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            try:
                usage = json.loads(data).get("usage")
            except ValueError:
                pass
        else:
            self.end_headers()
            while chunk := resp.read1(65536):
                self.wfile.write(chunk)
                self.wfile.flush()
                tail = (tail + chunk)[-8192:]
            for line in tail.decode("utf-8", "replace").splitlines():
                if line.startswith("data: {") and '"usage"' in line:
                    try:
                        usage = json.loads(line[6:]).get("usage") or usage
                    except ValueError:
                        pass
        self.close_connection = True
        return resp.status, usage

    def do_GET(self):
        self.forward("GET")

    def do_POST(self):
        raw = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        try:
            body = json.loads(raw or b"{}")
        except ValueError:
            body = {}
        started = time.time()
        result = self.forward("POST", raw)
        status, usage = result if result else (404, None)
        log({"t": started, "seconds": round(time.time() - started, 2), "path": self.path, "status": status,
             "model": body.get("model"), "stream": bool(body.get("stream")),
             "tools": [t.get("function", {}).get("name") for t in body.get("tools") or []],
             "messages": body.get("messages", []) if LOG_MESSAGES else len(body.get("messages") or []),
             "usage": usage})


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


if os.path.exists(SOCK):
    os.unlink(SOCK)
with Server(SOCK, Handler) as server:
    os.chmod(SOCK, 0o666)
    server.serve_forever()
