"""The photo service's AI Worker protocol (internal/aiworker on the Go side).

Every connection carries one request and one response. A frame is a 4-byte
big-endian length followed by JSON of at most MAX_FRAME bytes. An image
travels as one descriptor in the request's SCM_RIGHTS control message.
"""

import contextlib
import json
import os
import socket
import struct

MAX_FRAME = 1 << 20

# Describes the Worker: "model" and "dimensions" of its image vectors, the
# "detector", "faceModel" and "animalModel" it can use, and "memoryBytes", what
# it holds once all of them have run, its own start included, as its cgroup
# counts memory. With a text reader, also "ocrModel" and "ocrMemoryBytes":
# reading runs as a phase of its own, after the others are released.
OP_INFO = "info"
OP_EMBED_IMAGE = "embed_image"
# Encodes "text" as a search query; the provider adds the model's prompt.
OP_EMBED_QUERY = "embed_query"
# Finds the objects in the image; answers "model" and "boxes", each
# [class, score, x0, y0, x1, y1] with corners as fractions of the image. The
# caller cancels by hanging up, and then gets no answer.
OP_DETECT_OBJECTS = "detect_objects"
# Finds the faces in the image (docs/architecture/photo-people.md); answers
# "model" and "faces", each {"box": [x0, y0, x1, y1], "landmarks": [x, y] x 5,
# "score", "vector"} with coordinates as fractions of the image and the vector
# as base64 little-endian float32, L2-normalised. Hanging up cancels it too.
OP_DETECT_FACES = "detect_faces"
# Embeds the animals at "boxes" ([x0, y0, x1, y1] as fractions of the image,
# at most 16) to tell one pet from another (docs/architecture/photo-people.md);
# answers "model" and "vectors", one per box in order, each as base64
# little-endian float32, L2-normalised. Hanging up cancels it too.
OP_EMBED_ANIMALS = "embed_animals"
# Reads the text in the image (docs/architecture/photo-ocr.md): "kind" is
# "text" for the whole text, or "fields" with "schema", a JSON object whose
# empty values the model fills. Answers "model" and "reading": {"text", the
# answer; "logprobs", [token, log probability] for each token; "finish", why
# the answer ended: "stop", "length" or "repetition"}. Hanging up cancels it.
OP_READ_TEXT = "read_text"
# Frees memory: "what" is "detector" to drop the background models (the
# object detector, the face models, the animal model and the text reader), or
# "all" to exit after answering; systemd starts the Worker again when needed.
OP_RELEASE = "release"

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
    with contextlib.suppress(OSError):
        os.close(fd)
