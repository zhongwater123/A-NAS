"""Runs the AI Worker.

    python3 -m anas_ai.worker --model FILE --manifest FILE [--detector FILE --detector-manifest FILE] [--socket PATH]
    python3 -m anas_ai.worker --fake [--fake-detector] [--socket PATH]

Under systemd socket activation the listener arrives as descriptor 3 and
--socket is not needed. The Worker serves one connection at a time and exits
after IDLE_SECONDS without one, or when told to release everything, releasing
the models' memory; systemd starts it again on the next connection.

The image vector model is needed for search, so without it the Worker answers
"unavailable" to everything. The object detector only serves background work:
without it, only detect_objects is unavailable.
"""

import argparse
import base64
import logging
import os
import select
import socket
import sys
import threading

from anas_ai import protocol, providers

IDLE_SECONDS = 600
# The photo service sends thumbnails and a 1600 px detector input; anything
# larger is not what it sends.
MAX_IMAGE_BYTES = 32 << 20
# The photo service sends queries of at most 200 characters.
MAX_QUERY_CHARS = 1000
SYSTEMD_FIRST_FD = 3
# How often a running detection checks whether the caller hung up.
CANCEL_POLL_SECONDS = 0.05

log = logging.getLogger("anas-ai")


class Detector:
    """The object detector, or why there is none."""

    def __init__(self, provider=None, reason=None):
        self.provider = provider
        self.reason = reason


def listening_socket(path):
    if os.environ.get("LISTEN_PID") == str(os.getpid()) and os.environ.get("LISTEN_FDS") == "1":
        return socket.socket(fileno=SYSTEMD_FIRST_FD)
    if not path:
        raise SystemExit("--socket is required without systemd socket activation")
    try:
        os.unlink(path)
    except FileNotFoundError:
        pass
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    listener.bind(path)
    listener.listen(8)
    return listener


def serve(listener, provider, unavailable_reason=None, idle_seconds=IDLE_SECONDS, detector=None):
    """Answers connections until none arrives for idle_seconds or one asks it to release everything."""
    detector = detector or Detector(reason="no object detector")
    listener.settimeout(idle_seconds)
    while True:
        try:
            conn, _ = listener.accept()
        except TimeoutError:
            log.info("idle for %d s; exiting", idle_seconds)
            return
        with conn:
            conn.settimeout(None)
            if handle(conn, provider, unavailable_reason, detector):
                log.info("released everything; exiting")
                return


def handle(conn, provider, unavailable_reason, detector):
    """Answers one request; returns whether the Worker should exit."""
    try:
        request, fds = protocol.read_request(conn)
    except (protocol.ProtocolError, OSError, ValueError) as error:
        log.warning("dropped a malformed request: %s", error)
        return False
    try:
        response = respond(request, fds, provider, unavailable_reason, detector, conn)
    finally:
        for fd in fds:
            os.close(fd)
    if response is None:
        return False
    try:
        protocol.write_response(conn, response)
    except OSError as error:
        log.warning("could not answer: %s", error)
    return request.get("op") == protocol.OP_RELEASE and request.get("what") == "all" and response["ok"]


def respond(request, fds, provider, unavailable_reason, detector, conn=None):
    """The response to request, or None when the caller hung up before it was ready."""
    if unavailable_reason is not None:
        return protocol.failure(protocol.CODE_UNAVAILABLE, unavailable_reason)
    op = request.get("op")
    if op == protocol.OP_INFO:
        fields = {"model": provider.model, "dimensions": provider.dimensions}
        if detector.provider is not None:
            fields["detector"] = detector.provider.model
        return protocol.ok(**fields)
    if op == protocol.OP_EMBED_IMAGE:
        if len(fds) != 1:
            return protocol.failure(protocol.CODE_INVALID_INPUT, "embed_image needs exactly one image descriptor")
        return embedded(op, provider, lambda: provider.embed_image(read_limited(fds[0], MAX_IMAGE_BYTES)))
    if op == protocol.OP_EMBED_QUERY:
        text = request.get("text")
        if not isinstance(text, str) or not text.strip() or len(text) > MAX_QUERY_CHARS:
            return protocol.failure(protocol.CODE_INVALID_INPUT, f"embed_query needs text of 1 to {MAX_QUERY_CHARS} characters")
        return embedded(op, provider, lambda: provider.embed_query(text))
    if op == protocol.OP_DETECT_OBJECTS:
        if detector.provider is None:
            return protocol.failure(protocol.CODE_UNAVAILABLE, detector.reason)
        if len(fds) != 1:
            return protocol.failure(protocol.CODE_INVALID_INPUT, "detect_objects needs exactly one image descriptor")
        return detected(detector.provider, fds[0], conn)
    if op == protocol.OP_RELEASE:
        what = request.get("what")
        if what not in ("detector", "all"):
            return protocol.failure(protocol.CODE_INVALID_INPUT, 'release needs "what": "detector" or "all"')
        if detector.provider is not None:
            detector.provider.release()
        return protocol.ok()
    return protocol.failure(protocol.CODE_INTERNAL, f"unknown operation {op!r}")


def embedded(op, provider, embed):
    """Answers with the vector embed returns, or with why it failed."""
    try:
        vector = embed()
    except providers.InvalidInput as error:
        return protocol.failure(protocol.CODE_INVALID_INPUT, str(error))
    except providers.Unavailable as error:
        return protocol.failure(protocol.CODE_UNAVAILABLE, str(error))
    except Exception as error:  # pylint: disable=broad-except
        log.exception("%s failed", op)
        return protocol.failure(protocol.CODE_INTERNAL, type(error).__name__)
    return protocol.ok(
        model=provider.model,
        dimensions=len(vector),
        vector=base64.b64encode(providers.encode_vector(vector)).decode("ascii"),
    )


def detected(detector, fd, conn):
    """Answers with the boxes in the image, or None when the caller hung up.

    The detection runs on its own thread while this one watches the
    connection: when the photo service hangs up because the user became
    active, the detection is told to stop and the Worker is free at once.
    """
    try:
        data = read_limited(fd, MAX_IMAGE_BYTES)
    except providers.InvalidInput as error:
        return protocol.failure(protocol.CODE_INVALID_INPUT, str(error))
    run = detector.run_options()
    outcome = {}

    def work():
        try:
            outcome["boxes"] = detector.detect(data, run)
        except BaseException as error:  # pylint: disable=broad-except
            outcome["error"] = error

    thread = threading.Thread(target=work, name="detect", daemon=True)
    thread.start()
    while thread.is_alive():
        thread.join(CANCEL_POLL_SECONDS)
        if thread.is_alive() and conn is not None and not run.terminate and hung_up(conn):
            run.terminate = True
    error = outcome.get("error")
    if isinstance(error, providers.Cancelled) or run.terminate:
        log.info("detection cancelled")
        return None
    if isinstance(error, providers.InvalidInput):
        return protocol.failure(protocol.CODE_INVALID_INPUT, str(error))
    if isinstance(error, providers.Unavailable):
        return protocol.failure(protocol.CODE_UNAVAILABLE, str(error))
    if error is not None:
        log.error("detect_objects failed: %r", error)
        return protocol.failure(protocol.CODE_INTERNAL, type(error).__name__)
    return protocol.ok(model=detector.model, boxes=outcome["boxes"])


def hung_up(conn):
    """Whether the peer has closed the connection."""
    readable, _, _ = select.select([conn], [], [], 0)
    if not readable:
        return False
    try:
        return conn.recv(1, socket.MSG_PEEK | socket.MSG_DONTWAIT) == b""
    except BlockingIOError:
        return False
    except OSError:
        return True


def read_limited(fd, limit):
    chunks, size = [], 0
    while True:
        chunk = os.read(fd, 1 << 20)
        if not chunk:
            return b"".join(chunks)
        size += len(chunk)
        if size > limit:
            raise providers.InvalidInput(f"image larger than {limit} bytes")
        chunks.append(chunk)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--socket", help="listen here when not socket-activated")
    parser.add_argument("--model", help="the .litertlm model file")
    parser.add_argument("--manifest", help="the model manifest that pins its size and SHA-256")
    parser.add_argument("--vision-tokens", type=int, default=70, choices=providers.VISION_TOKENS)
    parser.add_argument("--cache-dir", help="a writable directory for the runtime's weight cache")
    parser.add_argument("--detector", help="the object detector's .onnx file")
    parser.add_argument("--detector-manifest", help="the detector manifest that pins its size and SHA-256")
    parser.add_argument("--threads", type=int, help="detector threads (default: the cores less the reserve)")
    parser.add_argument("--fake", action="store_true", help="serve deterministic fake vectors")
    parser.add_argument("--fake-detector", action="store_true", help="serve deterministic fake boxes")
    parser.add_argument("--idle-seconds", type=int, default=IDLE_SECONDS)
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s", stream=sys.stderr)

    provider, reason = None, None
    if args.fake:
        provider = providers.FakeProvider()
    elif args.model and args.manifest:
        try:
            provider = providers.MediaPipeProvider(args.model, args.manifest, args.vision_tokens, args.cache_dir)
        except providers.Unavailable as error:
            # Answer "unavailable" instead of exiting, so socket activation
            # does not restart a Worker that cannot load its model.
            reason = str(error)
            log.error("model unavailable: %s", reason)
    else:
        parser.error("give --fake, or --model and --manifest")
    detector = Detector(reason="no object detector")
    if args.fake_detector:
        detector = Detector(providers.FakeDetector())
    elif args.detector and args.detector_manifest:
        try:
            detector = Detector(providers.OnnxDetector(args.detector, args.detector_manifest, args.threads))
        except providers.Unavailable as error:
            detector = Detector(reason=str(error))
            log.error("object detector unavailable: %s", error)
    if provider is not None:
        log.info("serving %s (%d dimensions)", provider.model, provider.dimensions)
    if detector.provider is not None:
        log.info("detecting with %s (%s threads)", detector.provider.model, getattr(detector.provider, "threads", "-"))
    with listening_socket(args.socket) as listener:
        serve(listener, provider, reason, args.idle_seconds, detector)
    if provider is not None:
        provider.close()


if __name__ == "__main__":
    main()
