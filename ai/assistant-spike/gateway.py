"""Model gateway for the spike: the stand-in for the product service's gateway.

Listens on the Unix socket bound into the sandbox, forwards to an
OpenAI-compatible upstream and logs every request (tools offered, messages) and
the token usage it saw. With GATEWAY_LOG_MESSAGES=0 (the trial on the
Experimental NAS, where the requests carry members' files) it logs how many
messages there were instead of them.

UPSTREAM is where the model runs:
  api.deepseek.com          a cloud service over HTTPS; the gateway adds the key from
                            KEY_FILE, which only this process can read
  http://127.0.0.1:11434    NVIDIA PAIR on this NAS (its Ollama proxy), which sends the
                            request to a paired computer on the LAN that has the model;
                            or another plain-HTTP server, such as Ollama. It is never sent
                            the key, and KEY_FILE is "-"
"""
import http.client
import json
import os
import socketserver
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler

SOCK, LOG, UPSTREAM, KEY_FILE = sys.argv[1:5]
LOG_MESSAGES = os.environ.get("GATEWAY_LOG_MESSAGES", "1") != "0"

url = urllib.parse.urlsplit(UPSTREAM if "://" in UPSTREAM else "https://" + UPSTREAM)
if url.scheme not in ("http", "https") or not url.hostname or url.path not in ("", "/") \
        or url.query or url.fragment or url.username or url.password:
    sys.exit(f"gateway: upstream {UPSTREAM!r} is not a host or an http(s)://host[:port] URL")
SCHEME, HOST, PORT = url.scheme, url.hostname, url.port
KEY = "" if KEY_FILE == "-" else open(KEY_FILE, encoding="utf-8").read().strip()
if SCHEME == "https" and not KEY:
    sys.exit("gateway: an HTTPS upstream needs the key file")


def log(record):
    with open(LOG, "a", encoding="utf-8") as out:
        out.write(json.dumps(record, ensure_ascii=False) + "\n")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def address_string(self):
        return "uds"

    def fail(self, status, message):
        data = json.dumps({"error": {"message": message, "type": "upstream_unavailable"}}).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(data)
        self.close_connection = True

    def forward(self, method, body=b""):
        if self.path.startswith("/api/show"):
            self.send_response(404)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return 404, None
        kind = http.client.HTTPSConnection if SCHEME == "https" else http.client.HTTPConnection
        conn = kind(HOST, PORT, timeout=300)
        headers = {"Content-Type": "application/json", "Accept": self.headers.get("Accept", "*/*")}
        if SCHEME == "https":
            headers["Authorization"] = f"Bearer {KEY}"
        try:
            conn.request(method, self.path, body=body or None, headers=headers)
            resp = conn.getresponse()
        except (OSError, http.client.HTTPException) as err:
            # PAIR not running, or no paired computer that has the model answered in time.
            conn.close()
            self.fail(502, f"model gateway: {UPSTREAM}: {err}")
            return 502, None
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
            except (ValueError, AttributeError):
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
        conn.close()
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
        status, usage = self.forward("POST", raw)
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
