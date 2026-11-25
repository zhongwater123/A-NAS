"""The photo service's AI Worker protocol (internal/aiworker on the Go side).

Every connection carries one request and one response. A frame is a 4-byte
big-endian length followed by JSON of at most MAX_FRAME bytes. An image
travels as one descriptor in the request's SCM_RIGHTS control message;
describe_frames sends up to MAX_FDS, one per key frame, in order.
"""

import contextlib
import json
import os
import socket
import struct

MAX_FRAME = 1 << 20
# The most descriptors a request carries: describe_frames' key frames.
MAX_FDS = 20

# Describes the Worker: "model" and "dimensions" of its image vectors, the
# "detector", "faceModel" and "animalModel" it can use, and "memoryBytes", what
# it holds once all of them have run, its own start included, as its cgroup
# counts memory. With a text reader, also "ocrModel" and "ocrMemoryBytes":
# reading runs as a phase of its own, after the others are released. With an
# aesthetic scorer, also "aestheticModel" and "aestheticMemoryBytes", a phase
# of its own in the same way.
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
# Scores the image (docs/architecture/photo-ai.md#美学打分); answers "model"
# and "scores": {"aesthetic", "technical"}, each on 1 to 5 and not clipped.
# Hanging up cancels it.
OP_SCORE_AESTHETIC = "score_aesthetic"
# Describes a montage card (docs/architecture/photo-ai.md, 剪辑中心的分析) from
# its key frames, one descriptor each, 3 to MAX_FDS: "offsets", each frame's
# seconds from the card's start; "groups", the beat each frame belongs to,
# counted from 1 in order; "duration", the card's seconds. The prompt, the
# genre and the schema are the Worker's own, "template" in the answer;
# answers "model", "template" and "description", the JSON the schema allows.
# Each of its beats has "frames", the first and last of its key frames
# counted from 1 among those sent; a card with more than the reader's context
# holds is described from some of them, so it has fewer beats than frames.
# Hanging up cancels it.
OP_DESCRIBE_FRAMES = "describe_frames"
# Frees memory: "what" is "detector" to drop the background models (the
# object detector, the face models, the animal model, the text reader and the
# aesthetic scorer), or
# "all" to exit after answering; systemd starts the Worker again when needed.
OP_RELEASE = "release"

# The input cannot be processed; the same input would fail again.
CODE_INVALID_INPUT = "invalid_input"
# The Worker runs but cannot serve, for example without its model.
CODE_UNAVAILABLE = "unavailable"
# The request is malformed: too many descriptors, or fields out of bounds.
CODE_INVALID_REQUEST = "invalid_request"
# The model's answer did not fit the schema however often it was asked again.
CODE_INVALID_RESULT = "invalid_result"
# Anything else; the photo service spends an attempt and retries later.
CODE_INTERNAL = "internal"


class ProtocolError(Exception):
    """The peer broke the framing; the connection is dropped."""


class TooManyDescriptors(ProtocolError):
    """The request came with more descriptors than the receiver takes; it was read whole and can be answered."""


def read_request(conn, max_fds=1):
    """Returns the decoded request and the descriptors that came with it, at most max_fds.

    The kernel drops the descriptors past max_fds and says so; the request is
    then read to its end and refused with TooManyDescriptors.
    """
    data, fds, flags, _ = socket.recv_fds(conn, MAX_FRAME + 4, max_fds)
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
        if flags & socket.MSG_CTRUNC:
            raise TooManyDescriptors(f"more than {max_fds} descriptors")
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
