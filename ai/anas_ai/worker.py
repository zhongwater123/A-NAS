"""Runs the AI Worker.

    python3 -m anas_ai.worker --model FILE --manifest FILE [--socket PATH]
    python3 -m anas_ai.worker --fake [--socket PATH]

Under systemd socket activation the listener arrives as descriptor 3 and
--socket is not needed. The Worker serves one connection at a time and exits
after IDLE_SECONDS without one, releasing the model's memory; systemd starts
it again on the next connection.
"""

import argparse
import base64
import logging
import os
import socket
import sys

from anas_ai import protocol, providers

IDLE_SECONDS = 600
# Thumbnails are small; anything larger is not what the photo service sends.
MAX_IMAGE_BYTES = 32 << 20
SYSTEMD_FIRST_FD = 3

log = logging.getLogger("anas-ai")


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


def serve(listener, provider, unavailable_reason=None, idle_seconds=IDLE_SECONDS):
    """Answers connections until none arrives for idle_seconds."""
    listener.settimeout(idle_seconds)
    while True:
        try:
            conn, _ = listener.accept()
        except TimeoutError:
            log.info("idle for %d s; exiting", idle_seconds)
            return
        with conn:
            conn.settimeout(None)
            handle(conn, provider, unavailable_reason)


def handle(conn, provider, unavailable_reason):
    try:
        request, fds = protocol.read_request(conn)
    except (protocol.ProtocolError, OSError, ValueError) as error:
        log.warning("dropped a malformed request: %s", error)
        return
    try:
        response = respond(request, fds, provider, unavailable_reason)
    finally:
        for fd in fds:
            os.close(fd)
    try:
        protocol.write_response(conn, response)
    except OSError as error:
        log.warning("could not answer: %s", error)


def respond(request, fds, provider, unavailable_reason):
    if unavailable_reason is not None:
        return protocol.failure(protocol.CODE_UNAVAILABLE, unavailable_reason)
    op = request.get("op")
    if op == protocol.OP_INFO:
        return protocol.ok(model=provider.model, dimensions=provider.dimensions)
    if op == protocol.OP_EMBED_IMAGE:
        if len(fds) != 1:
            return protocol.failure(protocol.CODE_INVALID_INPUT, "embed_image needs exactly one image descriptor")
        try:
            data = read_limited(fds[0], MAX_IMAGE_BYTES)
            vector = provider.embed_image(data)
        except providers.InvalidInput as error:
            return protocol.failure(protocol.CODE_INVALID_INPUT, str(error))
        except providers.Unavailable as error:
            return protocol.failure(protocol.CODE_UNAVAILABLE, str(error))
        except Exception as error:  # pylint: disable=broad-except
            log.exception("embed_image failed")
            return protocol.failure(protocol.CODE_INTERNAL, type(error).__name__)
        return protocol.ok(
            model=provider.model,
            dimensions=len(vector),
            vector=base64.b64encode(providers.encode_vector(vector)).decode("ascii"),
        )
    return protocol.failure(protocol.CODE_INTERNAL, f"unknown operation {op!r}")


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
    parser.add_argument("--fake", action="store_true", help="serve deterministic fake vectors")
    parser.add_argument("--idle-seconds", type=int, default=IDLE_SECONDS)
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s", stream=sys.stderr)

    provider, reason = None, None
    if args.fake:
        provider = providers.FakeProvider()
    elif args.model and args.manifest:
        try:
            provider = providers.MediaPipeProvider(args.model, args.manifest, args.vision_tokens)
        except providers.Unavailable as error:
            # Answer "unavailable" instead of exiting, so socket activation
            # does not restart a Worker that cannot load its model.
            reason = str(error)
            log.error("model unavailable: %s", reason)
    else:
        parser.error("give --fake, or --model and --manifest")
    if provider is not None:
        log.info("serving %s (%d dimensions)", provider.model, provider.dimensions)
    with listening_socket(args.socket) as listener:
        serve(listener, provider, reason, args.idle_seconds)


if __name__ == "__main__":
    main()
