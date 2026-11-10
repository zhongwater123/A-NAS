"""Framing and descriptor ownership over real Unix stream sockets (#427)."""

import array
import contextlib
import errno
import json
import os
import socket
import struct
import tempfile
import threading
import unittest

from anas_ai import protocol


class ShortReads:
    """Keep the kernel from coalescing the fragments under test into one read."""

    def __init__(self, conn, limit):
        self.conn, self.limit = conn, limit
        self.fds = []
        self.reads = []

    def recvmsg(self, size, ancsize):
        result = self.conn.recvmsg(min(size, self.limit), ancsize)
        for level, kind, data in result[1]:
            if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
                descriptors = array.array("i")
                descriptors.frombytes(data)
                self.fds.extend(descriptors)
        return result

    def recv(self, size):
        self.reads.append(size)
        return self.conn.recv(min(size, self.limit))


class ProtocolTest(unittest.TestCase):
    def connection(self, frame, *, limit=1 << 21, descriptor=False):
        sender, receiver = socket.socketpair()
        self.enterContext(sender)
        self.enterContext(receiver)
        sender.settimeout(3)
        receiver.settimeout(3)
        conn = ShortReads(receiver, limit)
        # Also close leaked descriptors after a failing assertion, so mutations
        # cannot exhaust the test runner's descriptor table.
        self.addCleanup(self.close_received, conn)
        errors = []
        image = self.enterContext(tempfile.TemporaryFile()) if descriptor else None

        def send():
            try:
                if image is not None:
                    socket.send_fds(sender, [frame[:1]], [image.fileno()])
                    rest = frame[1:]
                else:
                    rest = frame
                sender.sendall(rest)
                sender.shutdown(socket.SHUT_WR)
            except OSError as error:
                errors.append(error)

        thread = threading.Thread(target=send, daemon=True)
        thread.start()
        self.addCleanup(self.join_sender, thread, errors)
        return conn, image

    def join_sender(self, thread, errors):
        thread.join(4)
        self.assertFalse(thread.is_alive(), "sender did not finish")
        self.assertEqual(errors, [])

    @staticmethod
    def close_received(conn):
        for fd in conn.fds:
            with contextlib.suppress(OSError):
                os.close(fd)

    def assert_closed(self, conn):
        self.assertEqual(len(conn.fds), 1)
        with self.assertRaises(OSError) as caught:
            os.fstat(conn.fds[0])
        self.assertEqual(caught.exception.errno, errno.EBADF)
        conn.fds.clear()  # the number may be reused by the next subtest

    def test_reassembles_header_and_body_and_transfers_descriptor_ownership(self):
        payload = '{"op":"embed_query","text":"海边的猫"}'.encode()
        frame = struct.pack(">I", len(payload)) + payload
        conn, image = self.connection(frame, limit=1, descriptor=True)
        image.write(b"still open")
        image.seek(0)
        request, fds = protocol.read_request(conn)
        self.assertEqual(request, {"op": "embed_query", "text": "海边的猫"})
        self.assertEqual(fds, conn.fds)
        self.assertEqual(len(fds), 1)
        self.assertEqual(os.read(fds[0], 20), b"still open")
        self.assertEqual(conn.reads[:3], [3, 2, 1])
        self.assertGreater(len(conn.reads), 4)
        self.assertTrue(all(size > 0 for size in conn.reads))

    def test_accepts_exactly_max_frame_bytes(self):
        payload = b'{"text":"' + b"x" * (protocol.MAX_FRAME - 11) + b'"}'
        self.assertEqual(len(payload), protocol.MAX_FRAME)
        conn, _ = self.connection(struct.pack(">I", len(payload)) + payload)
        request, fds = protocol.read_request(conn)
        self.assertEqual(request, {"text": "x" * (protocol.MAX_FRAME - 11)})
        self.assertEqual(fds, [])

    def test_rejects_oversize_header_and_closes_descriptor_without_reading_body(self):
        conn, _ = self.connection(struct.pack(">I", protocol.MAX_FRAME + 1), descriptor=True)
        with self.assertRaises(protocol.ProtocolError):
            protocol.read_request(conn)
        self.assert_closed(conn)

    def test_closes_descriptor_on_truncated_header_body_and_invalid_json(self):
        for frame, error in (
            (b"\x00\x00", protocol.ProtocolError),
            (struct.pack(">I", 10) + b"{}", protocol.ProtocolError),
            (struct.pack(">I", 1) + b"{", json.JSONDecodeError),
            (struct.pack(">I", 1) + b"\xff", UnicodeDecodeError),
            (struct.pack(">I", 2) + b"[]", protocol.ProtocolError),
            (struct.pack(">I", 0), json.JSONDecodeError),
        ):
            with self.subTest(frame=frame):
                conn, image = self.connection(frame, limit=1, descriptor=True)
                with self.assertRaises(error):
                    protocol.read_request(conn)
                self.assert_closed(conn)
                os.fstat(image.fileno())  # the sender's original is still owned by it

    def test_rejects_eof_without_a_header(self):
        conn, _ = self.connection(b"")
        with self.assertRaises(protocol.ProtocolError):
            protocol.read_request(conn)
