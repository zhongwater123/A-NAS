"""One call to the executor of the assistant's changes from a shell script.

  broker_client.py product METHOD PATH [JSON]   as the product service (PATH carries
                                                ?account=<member>)
  broker_client.py agent METHOD PATH [JSON]     as a member's assistant (the agent socket;
                                                run it as the member, the executor reads the UID)

Prints the HTTP status, a space, and the response body. A product call carries the member's
session from BROKER_SESSION when it is set, as the product service does.

The executor is the spike's change_broker.py, or with EXECUTOR=host-agent the Host Agent's
File Broker (ADR 0024 decision 10): product calls then go over file-broker.sock in the
broker's own framing (a 4-byte length, then JSON), and answer as the HTTP broker would.
"""
import http.client
import json
import os
import socket
import struct
import sys

SOCKETS = {"product": "/run/anas-spike-broker/broker.sock", "agent": "/run/anas-change-agent/agent.sock"}
FILE_BROKER = "/run/a-nas/file-broker.sock"
STATUS = {"change_invalid": 422, "change_decided": 409, "not_found": 404, "unauthenticated": 401}


class UDS(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("uds", timeout=60)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(60)
        self.sock.connect(self.path)


def _exact(sock, n):
    data = b""
    while len(data) < n:
        chunk = sock.recv(n - len(data))
        if not chunk:
            raise ConnectionError("the File Broker closed the connection")
        data += chunk
    return data


def file_broker(method, path, body, session):
    """A product call to the File Broker's executor, answered as (status, body)."""
    parts = path.split("?")[0].strip("/").split("/")
    body = body or {}
    if method == "POST" and parts == ["changes", "prepare"]:
        change = {"sessionId": body.get("sessionId", ""), "requestId": body.get("requestId", ""),
                  "arguments": {k: body[k] for k in ("summary", "operations") if k in body}}
        op = "change_prepare"
    elif method == "POST" and len(parts) == 3 and parts[0] == "changes" and parts[2] in ("approve", "reject", "expire"):
        op, change = "change_decide", {"id": parts[1], "decision": parts[2]}
    elif method == "GET" and len(parts) == 2 and parts[0] == "changes":
        op, change = "change_get", {"id": parts[1]}
    else:
        return 404, {"error": "not found"}
    data = json.dumps({"token": session, "op": op, "change": change}, ensure_ascii=False).encode()
    with socket.socket(socket.AF_UNIX) as sock:
        sock.settimeout(60)
        sock.connect(FILE_BROKER)
        sock.sendall(struct.pack(">I", len(data)) + data)
        reply = json.loads(_exact(sock, struct.unpack(">I", _exact(sock, 4))[0]))
    error = reply.get("error")
    if error:
        return STATUS.get(error.get("code"), 500), {"error": error.get("message") or error.get("code")}
    return 200, reply.get("changeCard") or {}


def product(method, path, body=None, session=None):
    session = os.environ.get("BROKER_SESSION", "") if session is None else session
    if os.environ.get("EXECUTOR") == "host-agent":
        return file_broker(method, path, body, session)
    conn = UDS(SOCKETS["product"])
    headers = {"Content-Type": "application/json", **({"X-A-NAS-Session": session} if session else {})}
    conn.request(method, path, json.dumps(body, ensure_ascii=False).encode() if body is not None else None, headers)
    reply = conn.getresponse()
    data = reply.read()
    try:
        return reply.status, json.loads(data or b"{}")
    except ValueError:
        return reply.status, data.decode(errors="replace")


def agent(method, path, body=None):
    conn = UDS(SOCKETS["agent"])
    conn.request(method, path, json.dumps(body, ensure_ascii=False).encode() if body is not None else None,
                 {"Content-Type": "application/json"})
    reply = conn.getresponse()
    return reply.status, reply.read().decode()


if __name__ == "__main__":
    side, method, path = sys.argv[1:4]
    body = json.loads(sys.argv[4]) if len(sys.argv) > 4 and sys.argv[4] else None
    status, answer = product(method, path, body) if side == "product" else agent(method, path, body)
    print(status, answer if isinstance(answer, str) else json.dumps(answer, ensure_ascii=False))
