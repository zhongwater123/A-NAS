#!/usr/bin/env python3
"""Read-only release probes; run as the Product Service identity for UDS checks.

Routes are defined by internal/containers/agent, internal/appstore/agent and
internal/webui. The public /api/v1/containers route is NOT the agent protocol.
"""

import argparse
import http.client
import json
import socket
import sys


class UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("container-agent", timeout=20)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def request(connection, path, expected=200, headers=None, limit=4 * 1024 * 1024):
    try:
        connection.request("GET", path, headers=headers or {})
        response = connection.getresponse()
        body = response.read(limit + 1)
        if response.status != expected:
            raise ValueError(f"HTTP {response.status}, expected {expected}")
        if len(body) > limit:
            raise ValueError("response exceeds probe size limit")
        print(f"{path}: HTTP {response.status}, {len(body)} bytes", flush=True)
        return body, dict(response.getheaders())
    except (OSError, ValueError, http.client.HTTPException) as error:
        raise ValueError(f"{path}: {error}") from error
    finally:
        connection.close()


def verify_agent(socket_path):
    body, _ = request(UnixHTTPConnection(socket_path), "/v1/snapshot")
    snapshot = json.loads(body)
    if not isinstance(snapshot, dict):
        raise ValueError("/v1/snapshot: expected JSON object")
    engine = snapshot.get("engine")
    if not isinstance(engine, dict) or not engine.get("version") or not engine.get("apiVersion"):
        raise ValueError("/v1/snapshot: missing Docker engine version")
    if not all(isinstance(snapshot.get(key), list) for key in ("containers", "images")):
        raise ValueError("/v1/snapshot: missing container/image arrays")
    print(f"Docker={engine['version']} API={engine['apiVersion']} "
          f"containers={len(snapshot['containers'])} images={len(snapshot['images'])}", flush=True)

    body, _ = request(UnixHTTPConnection(socket_path), "/v1/apps")
    catalog = json.loads(body)
    if not isinstance(catalog, dict) or not isinstance(catalog.get("apps"), list) or not catalog["apps"]:
        raise ValueError("/v1/apps: missing/nonempty app catalog required")
    print(f"app_catalog={len(catalog['apps'])}", flush=True)


def verify_desktop(port):
    def connection():
        return http.client.HTTPConnection("127.0.0.1", port, timeout=5)

    body, _ = request(connection(), "/healthz")
    if json.loads(body) != {"status": "ok"}:
        raise ValueError("/healthz: unexpected health payload")
    body, _ = request(connection(), "/")
    if b"<html" not in body or b"/assets/" not in body:
        raise ValueError("/: embedded desktop document missing")
    body, headers = request(connection(), "/local-console/screensaver.mp4", expected=206,
                            headers={"Range": "bytes=0-1023"}, limit=1024)
    response_headers = {key.lower(): value for key, value in headers.items()}
    if len(body) != 1024 or not response_headers.get("content-range", "").startswith("bytes 0-1023/"):
        raise ValueError("/local-console/screensaver.mp4: invalid byte range response")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--agent-only", action="store_true")
    mode.add_argument("--desktop-only", action="store_true")
    parser.add_argument("--socket", default="/run/a-nas-container/agent.sock")
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()
    try:
        if args.agent_only:
            verify_agent(args.socket)
        else:
            verify_desktop(args.port)
    except (ValueError, OSError, http.client.HTTPException) as error:
        print(f"FAIL: {error}", file=sys.stderr, flush=True)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
