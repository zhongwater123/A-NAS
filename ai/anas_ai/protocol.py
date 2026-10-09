"""The photo service's AI Worker protocol (internal/aiworker on the Go side).

Every connection carries one request and one response. A frame is a 4-byte
big-endian length followed by JSON of at most MAX_FRAME bytes. An image
travels as one descriptor in the request's SCM_RIGHTS control message.
"""

import json
import os
import socket
import struct

MAX_FRAME = 1 << 20

OP_INFO = "info"
OP_EMBED_IMAGE = "embed_image"

# The input cannot be processed; the same input would fail again.
CODE_INVALID_INPUT = "invalid_input"
# The Worker runs but cannot serve, for example without its model.
CODE_UNAVAILABLE = "unavailable"
# Anything else; the photo service spends an attempt and retries later.
CODE_INTERNAL = "internal"


class ProtocolError(Exception):
    """The peer broke the framing; the connection is dropped."""


def read_request(conn):
    """Returns the decoded request and the descriptors that came with it."""
    data, fds, _, _ = socket.recv_fds(conn, MAX_FRAME + 4, 1)
    try:
        while len(data) < 4:
            more = conn.recv(4 - len(data))
            if not more:
                raise ProtocolError("connection closed before the frame header")
            data += more
        (length,) = struct.unpack(">I", data[:4])
        if length > MAX_FRAME:
            raise ProtocolError(f"request of {length} bytes exceeds the limit")
        while len(data) < 4 + length:
            more = conn.recv(4 + length - len(data))
            if not more:
                raise ProtocolError("connection closed inside the frame")
            data += more
        request = json.loads(data[4 : 4 + length])
        if not isinstance(request, dict):
            raise ProtocolError("request is not a JSON object")
        return request, fds
    except BaseException:
        for fd in fds:
            _close(fd)
        raise


def write_response(conn, response):
    payload = json.dumps(response, separators=(",", ":")).encode("utf-8")
    if len(payload) > MAX_FRAME:
        raise ProtocolError(f"response of {len(payload)} bytes exceeds the limit")
    conn.sendall(struct.pack(">I", len(payload)) + payload)


def ok(**fields):
    return {"ok": True, **fields}


def failure(code, message):
    return {"ok": False, "error": {"code": code, "message": message}}


def _close(fd):
    try:
        os.close(fd)
    except OSError:
        pass
