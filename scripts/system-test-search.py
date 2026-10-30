"""A stand-in for SearXNG in the system test (scripts/system-test.sh).

It runs under anas-search.service in place of Granian, so the unit's own
sandbox applies, and answers on the same socket: /healthz, POST /search with
one fixed result, /image with a small PNG, and /probe?host=&port=, which
reports whether the sandbox let it open a TCP connection there ("connected",
"refused", "denied" for EPERM, "timeout" or another errno name).

Usage: system-test-search.py SOCKET
"""

import errno
import http.server
import json
import os
import socket
import socketserver
import sys
import urllib.parse

PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d4948445200000001000000010806000000"
    "1f15c4890000000d49444154789c6360000002000154a24f5d0000000049454e44ae426082"
)


def probe(host, port):
    try:
        with socket.create_connection((host, port), timeout=2):
            return "connected"
    except ConnectionRefusedError:
        return "refused"
    except TimeoutError:
        return "timeout"
    except OSError as error:
        if error.errno == errno.EPERM:
            return "denied"
        return errno.errorcode.get(error.errno or 0, repr(error))


class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self, status, body, content_type):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        query = urllib.parse.parse_qs(url.query)
        if url.path == "/healthz":
            self.reply(200, b"OK", "text/plain")
        elif url.path == "/probe":
            result = probe(query["host"][0], int(query["port"][0]))
            self.reply(200, json.dumps({"result": result}).encode(), "application/json")
        elif url.path == "/image":
            self.reply(200, PNG, "image/png")
        else:
            self.reply(404, b"", "text/plain")

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        form = urllib.parse.parse_qs(self.rfile.read(length).decode())
        if self.path != "/search" or form.get("format") != ["json"]:
            self.reply(403, b"", "text/plain")
            return
        query = form.get("q", [""])[0]
        result = {
            "url": "https://example.org/a-nas",
            "title": f"System test: {query}",
            "content": "A stand-in result.",
            "engine": "sogou",
            "engines": ["sogou"],
            "category": "general",
            "template": "default.html",
        }
        body = {
            "query": query,
            "results": [result],
            "answers": [],
            "infoboxes": [],
            "suggestions": [],
            "corrections": [],
            "unresponsive_engines": [["baidu", "timeout"]],
        }
        self.reply(200, json.dumps(body).encode(), "application/json")

    def log_message(self, format, *args):  # noqa: A002 - the base class names it
        # Like Granian's default: no access log.
        pass


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True

    def get_request(self):
        request, _ = super().get_request()
        # BaseHTTPRequestHandler expects an address pair.
        return request, ("local", 0)


path = sys.argv[1]
if os.path.exists(path):
    os.unlink(path)
server = Server(path, Handler)
os.chmod(path, 0o660)
server.serve_forever()
