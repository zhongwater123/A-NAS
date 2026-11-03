"""Model gateway for the spike: the stand-in for the product service's gateway.

Listens on the Unix socket bound into the sandbox, adds the provider key that
only this process can read, forwards to an OpenAI-compatible upstream and logs
every request (tools offered, messages) and the token usage it saw. With
GATEWAY_LOG_MESSAGES=0 (the trial on the Experimental NAS, where the requests
carry members' files) it logs how many messages there were instead of them.
"""
import argparse
import contextlib
import http.client
import ipaddress
import json
import os
import socketserver
import time
from http.server import BaseHTTPRequestHandler
from pathlib import Path
from urllib.parse import urlsplit


def loopback_host(host):
    if host and host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def upstream_base(value):
    """A legacy HTTPS host or a full OpenAI-compatible /v1 base URL.

    Local HTTP is deliberately limited to loopback. The gateway, not a model
    or a browser, owns this operator-supplied setting and the real credential.
    """
    if not value or value != value.strip() or any(c.isspace() for c in value) or "\\" in value:
        raise ValueError("invalid model base URL")
    base = urlsplit(value if "://" in value else f"https://{value}/v1")
    if (base.scheme not in {"http", "https"} or not base.hostname or
            base.username is not None or base.password is not None or base.query or base.fragment):
        raise ValueError("model base URL must not contain credentials, query or fragment")
    # Force validation of the port even before making a connection.
    if base.port is not None and not 1 <= base.port <= 65535:
        raise ValueError("invalid model port")
    if base.scheme == "http" and not loopback_host(base.hostname):
        raise ValueError("unencrypted model HTTP must use a loopback address")
    return base


def upstream_path(base, incoming):
    """Replace Hermes's /v1 prefix; retain provider-specific base paths."""
    request = urlsplit(incoming)
    if request.scheme or request.netloc or request.fragment:
        raise ValueError("model request must be an origin-relative path")
    if request.path != "/v1" and not request.path.startswith("/v1/"):
        raise ValueError("model request must be under /v1")
    path = base.path.rstrip("/") + request.path[3:]
    return (path or "/") + (f"?{request.query}" if request.query else "")


def https_proxy(value):
    """Explicit operator-owned HTTP CONNECT proxy; no URL secrets or paths."""
    if not value:
        return None
    if value != value.strip() or any(c.isspace() for c in value) or "\\" in value:
        raise ValueError("invalid model HTTPS proxy")
    proxy = urlsplit(value)
    if (proxy.scheme != "http" or not proxy.hostname or proxy.path not in {"", "/"} or
            proxy.username is not None or proxy.password is not None or proxy.query or proxy.fragment):
        raise ValueError("model HTTPS proxy must be an HTTP origin without credentials")
    if proxy.port is not None and not 1 <= proxy.port <= 65535:
        raise ValueError("invalid model HTTPS proxy port")
    return proxy


def model_connection(base, proxy):
    # Never send local model traffic into a cloud proxy. HTTPSConnection uses
    # the stdlib's CONNECT implementation and verifies the upstream's TLS name.
    if proxy is not None and base.scheme == "https" and not loopback_host(base.hostname):
        connection = http.client.HTTPSConnection(proxy.hostname, proxy.port or 80, timeout=300)
        connection.set_tunnel(base.hostname, base.port or 443)
        return connection
    connection = http.client.HTTPConnection if base.scheme == "http" else http.client.HTTPSConnection
    return connection(base.netloc, timeout=300)


def log(server, record):
    with open(server.log_path, "a", encoding="utf-8") as out:
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
        base = self.server.upstream
        try:
            path = upstream_path(base, self.path)
        except ValueError:
            self.send_error(400, "Unsupported model request path")
            return 400, None
        upstream = model_connection(base, self.server.https_proxy)
        headers = {"Content-Type": "application/json", "Accept": self.headers.get("Accept", "*/*")}
        headers["User-Agent"] = self.headers.get("User-Agent", "A-NAS-Model-Gateway")
        if self.server.api_key:
            headers["Authorization"] = f"Bearer {self.server.api_key}"
        upstream.request(method, path, body=body or None, headers=headers)
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
            with contextlib.suppress(ValueError):
                usage = json.loads(data).get("usage")
        else:
            self.end_headers()
            while chunk := resp.read1(65536):
                self.wfile.write(chunk)
                self.wfile.flush()
                tail = (tail + chunk)[-8192:]
            for line in tail.decode("utf-8", "replace").splitlines():
                if line.startswith("data: {") and '"usage"' in line:
                    with contextlib.suppress(ValueError):
                        usage = json.loads(line[6:]).get("usage") or usage
        self.close_connection = True
        upstream.close()
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
        log(self.server, {"t": started, "seconds": round(time.time() - started, 2), "path": self.path, "status": status,
             "model": body.get("model"), "stream": bool(body.get("stream")),
             "tools": [t.get("function", {}).get("name") for t in body.get("tools") or []],
             "messages": body.get("messages", []) if self.server.log_messages else len(body.get("messages") or []),
             "usage": usage})


class Server(socketserver.ThreadingMixIn, socketserver.UnixStreamServer):
    daemon_threads = True


def main():
    parser = argparse.ArgumentParser(description="A-NAS OpenAI-compatible model gateway")
    parser.add_argument("--check-upstream", metavar="BASE_URL", help="validate configuration without reading a key or connecting")
    for name in ("sock", "log_path", "upstream", "key_file"):
        parser.add_argument(name, nargs="?")
    args = parser.parse_args()
    try:
        base = upstream_base(args.check_upstream if args.check_upstream is not None else args.upstream)
    except ValueError:
        parser.error("invalid model base URL; use HTTPS or loopback HTTP without credentials/query/fragment")
    if args.check_upstream is not None:
        return
    if any(value is None for value in (args.sock, args.log_path, args.upstream, args.key_file)):
        parser.error("provide socket, log path, upstream and key file")
    sock, log_path, key_file = args.sock, args.log_path, args.key_file
    proxy = https_proxy(os.environ.get("GATEWAY_HTTPS_PROXY", ""))
    key = None
    if os.environ.get("GATEWAY_LOCAL_NO_AUTH") == "1":
        if not loopback_host(base.hostname):
            raise ValueError("no-auth model mode requires a loopback address")
    else:
        key = Path(key_file).read_text(encoding="utf-8").strip()
        if not key or "\r" in key or "\n" in key:
            raise ValueError("model credential must be a nonempty single line")
    if os.path.exists(sock):
        os.unlink(sock)
    with Server(sock, Handler) as server:
        server.upstream = base
        server.https_proxy = proxy
        server.api_key = key
        server.log_path = log_path
        server.log_messages = os.environ.get("GATEWAY_LOG_MESSAGES", "1") != "0"
        os.chmod(sock, 0o666)  # noqa: S103  the sandboxed assistant connects as another user
        server.serve_forever()


if __name__ == "__main__":
    main()
