"""The assistants' model gateway (ADR 0024 decision 7): anas-assistant-gateway.service.

  gateway.py SOCKET LOG

Listens on SOCKET, which each member's bridge (anas-assistant-net@.service) reaches as the
member, adds the administrator's DeepSeek key that only this process can read, and forwards
the two requests Hermes makes of an OpenAI-compatible model to DeepSeek. Every request is
logged to LOG with the member it came from (the peer's UID), the tools offered, how many
messages there were and the token usage; the messages themselves carry members' files and
are not logged.

The key is the systemd credential a-nas-assistant-deepseek ($CREDENTIALS_DIRECTORY), which
the Host Agent encrypts when the administrator saves it. The spike and the system test, which
have no administrator, name a key file in ANAS_ASSISTANT_KEY_FILE and their stand-in model in
ANAS_ASSISTANT_UPSTREAM (http://HOST:PORT); the spike's research also logs the messages, with
GATEWAY_LOG_MESSAGES=1.
"""
import contextlib
import grp
import http.client
import json
import os
import pwd
import socket
import socketserver
import struct
import sys
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler
from pathlib import Path

SOCK, LOG = sys.argv[1:3]
CREDENTIAL = "a-nas-assistant-deepseek"
KEY_FILE = os.environ.get("ANAS_ASSISTANT_KEY_FILE") or os.path.join(os.environ.get("CREDENTIALS_DIRECTORY", ""), CREDENTIAL)
KEY = Path(KEY_FILE).read_text(encoding="utf-8").strip()
UPSTREAM = urllib.parse.urlsplit(os.environ.get("ANAS_ASSISTANT_UPSTREAM", "https://api.deepseek.com"))
LOG_MESSAGES = os.environ.get("GATEWAY_LOG_MESSAGES", "0") == "1"
MEMBERS = "a-nas-users"
# What Hermes asks of the model (model.base_url in the managed policy ends in /v1); nothing else
# reaches DeepSeek with the key.
ALLOWED = {("POST", "/v1/chat/completions"), ("GET", "/v1/models")}
LIMIT = 64 << 20


def log(record):
    with open(LOG, "a", encoding="utf-8") as out:
        out.write(json.dumps(record, ensure_ascii=False) + "\n")


def peer_account(conn):
    """The account of the process on the other end of the Unix socket, or its UID."""
    with contextlib.suppress(OSError):
        _, uid, _ = struct.unpack("3i", conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, struct.calcsize("3i")))
        try:
            return pwd.getpwuid(uid).pw_name
        except KeyError:
            return str(uid)
    return None


def upstream_connection():
    host = UPSTREAM.hostname or ""
    if UPSTREAM.scheme == "http":
        return http.client.HTTPConnection(host, UPSTREAM.port or 80, timeout=300)
    return http.client.HTTPSConnection(host, UPSTREAM.port or 443, timeout=300)


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass

    def address_string(self):
        return "uds"

    def refuse(self, status):
        self.send_response(status)
        self.send_header("Content-Length", "0")
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True

    def forward(self, method, body=b""):
        upstream = upstream_connection()
        headers = {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json", "Accept": self.headers.get("Accept", "*/*")}
        upstream.request(method, urllib.parse.urlsplit(self.path).path, body=body or None, headers=headers)
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
            with contextlib.suppress(ValueError, AttributeError):
                usage = json.loads(data).get("usage")
        else:
            self.end_headers()
            while chunk := resp.read1(65536):
                self.wfile.write(chunk)
                self.wfile.flush()
                tail = (tail + chunk)[-8192:]
            for line in tail.decode("utf-8", "replace").splitlines():
                if line.startswith("data: {") and '"usage"' in line:
                    with contextlib.suppress(ValueError, AttributeError):
                        usage = json.loads(line[6:]).get("usage") or usage
        self.close_connection = True
        return resp.status, usage

    def handle_request(self, method):
        started = time.time()
        path = urllib.parse.urlsplit(self.path).path
        record = {"t": started, "account": peer_account(self.request), "method": method, "path": path}
        length = int(self.headers.get("Content-Length", "0") or "0")
        if (method, path) not in ALLOWED or not 0 <= length <= LIMIT:
            self.refuse(404 if length <= LIMIT else 413)
            log({**record, "status": "refused"})
            return
        raw = self.rfile.read(length) if length else b""
        try:
            body = json.loads(raw or b"{}")
        except ValueError:
            body = {}
        if not isinstance(body, dict):
            body = {}
        try:
            status, usage = self.forward(method, raw)
        except OSError as error:
            log({**record, "status": "unreachable", "error": type(error).__name__})
            with contextlib.suppress(OSError):
                self.refuse(502)
            return
        tools = body.get("tools") if isinstance(body.get("tools"), list) else []
        messages = body.get("messages") if isinstance(body.get("messages"), list) else []
        log({**record, "seconds": round(time.time() - started, 2), "status": status, "model": body.get("model"),
             "stream": bool(body.get("stream")),
             "tools": [t.get("function", {}).get("name") for t in tools if isinstance(t, dict)],
             "messages": messages if LOG_MESSAGES else len(messages), "usage": usage})

    def do_GET(self):
        self.handle_request("GET")

    def do_POST(self):
        self.handle_request("POST")

    def do_PUT(self):
        self.handle_request("PUT")

    def do_DELETE(self):
        self.handle_request("DELETE")


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


if os.path.exists(SOCK):
    os.unlink(SOCK)
with Server(SOCK, Handler) as server:
    # Only members' bridges connect; each runs as the member, in the group a-nas-users.
    os.chown(SOCK, -1, grp.getgrnam(MEMBERS).gr_gid)
    os.chmod(SOCK, 0o660)
    server.serve_forever()
