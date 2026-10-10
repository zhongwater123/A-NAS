"""Protocol tests of the AI Worker with the fake provider (standard library only)."""

import base64
import hashlib
import importlib.util
import json
import os
import shutil
import socket
import struct
import tempfile
import threading
import time
import unittest

from anas_ai import protocol, providers, worker


def call(path, request, image=None):
    """Sends one request the way internal/aiworker does and returns the response."""
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.connect(path)
        payload = json.dumps(request).encode()
        frame = struct.pack(">I", len(payload)) + payload
        if image is None:
            conn.sendall(frame)
        else:
            socket.send_fds(conn, [frame], [image.fileno()])
        header = b""
        while len(header) < 4:
            header += conn.recv(4 - len(header))
        (length,) = struct.unpack(">I", header)
        body = b""
        while len(body) < length:
            body += conn.recv(length - len(body))
        return json.loads(body)


class Failing:
    """A provider whose image embedding raises error."""

    model, dimensions = "failing", 4

    def __init__(self, error):
        self.error = error

    def embed_image(self, data):
        raise self.error


class WorkerTest(unittest.TestCase):
    def start(self, provider, reason=None, idle_seconds=30):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        path = os.path.join(directory, "ai.sock")
        listener = worker.listening_socket(path)
        self.addCleanup(listener.close)
        thread = threading.Thread(target=worker.serve, args=(listener, provider, reason, idle_seconds), daemon=True)
        thread.start()
        return path, thread

    def image(self, content):
        file = tempfile.TemporaryFile()
        file.write(content)
        file.seek(0)
        self.addCleanup(file.close)
        return file

    def test_info_and_deterministic_image_vectors(self):
        path, _ = self.start(providers.FakeProvider(dimensions=8))
        self.assertEqual(call(path, {"op": "info"}), {"ok": True, "model": "fake-sha256-8", "dimensions": 8})

        first = call(path, {"op": "embed_image"}, self.image(b"thumbnail bytes"))
        again = call(path, {"op": "embed_image"}, self.image(b"thumbnail bytes"))
        other = call(path, {"op": "embed_image"}, self.image(b"another thumbnail"))
        self.assertTrue(first["ok"])
        self.assertEqual(first["vector"], again["vector"])
        self.assertNotEqual(first["vector"], other["vector"])
        vector = struct.unpack("<8f", base64.b64decode(first["vector"]))
        self.assertAlmostEqual(sum(value * value for value in vector), 1.0, places=5)

        query = call(path, {"op": "embed_query", "text": "海边的猫"})
        self.assertTrue(query["ok"])
        self.assertEqual(query["vector"], call(path, {"op": "embed_query", "text": "海边的猫"})["vector"])
        self.assertNotEqual(query["vector"], call(path, {"op": "embed_query", "text": "车库里的电动车"})["vector"])

    def test_rejects_requests_it_cannot_serve(self):
        path, _ = self.start(providers.FakeProvider())
        self.assertEqual(call(path, {"op": "embed_image"})["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "embed_image"}, self.image(b""))["error"]["code"], protocol.CODE_INVALID_INPUT)
        self.assertEqual(call(path, {"op": "embed_text"})["error"]["code"], protocol.CODE_INTERNAL)
        for request in ({"op": "embed_query"}, {"op": "embed_query", "text": "  "},
                        {"op": "embed_query", "text": 7}, {"op": "embed_query", "text": "猫" * (worker.MAX_QUERY_CHARS + 1)}):
            self.assertEqual(call(path, request)["error"]["code"], protocol.CODE_INVALID_INPUT, request)

        failing, _ = self.start(Failing(providers.InvalidInput("not an image")))
        self.assertEqual(call(failing, {"op": "embed_image"}, self.image(b"x"))["error"]["code"], protocol.CODE_INVALID_INPUT)
        crashing, _ = self.start(Failing(MemoryError()))
        response = call(crashing, {"op": "embed_image"}, self.image(b"x"))
        self.assertEqual(response["error"], {"code": protocol.CODE_INTERNAL, "message": "MemoryError"})

    def test_answers_unavailable_without_its_model(self):
        path, _ = self.start(None, reason="model file missing")
        for request in ({"op": "info"}, {"op": "embed_image"}, {"op": "embed_query", "text": "猫"}):
            self.assertEqual(call(path, request)["error"]["code"], protocol.CODE_UNAVAILABLE)

    def test_exits_when_idle_and_survives_bad_frames(self):
        path, thread = self.start(providers.FakeProvider(), idle_seconds=1)
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
            conn.connect(path)
            conn.sendall(struct.pack(">I", protocol.MAX_FRAME + 1))
        self.assertTrue(call(path, {"op": "info"})["ok"])
        started = time.monotonic()
        thread.join(timeout=10)
        self.assertFalse(thread.is_alive())
        self.assertLess(time.monotonic() - started, 10)


class ManifestTest(unittest.TestCase):
    def test_only_the_pinned_file_passes(self):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        model = os.path.join(directory, "model.litertlm")
        with open(model, "wb") as file:
            file.write(b"weights")
        manifest = os.path.join(directory, "manifest.json")

        def pin(size, digest):
            with open(manifest, "w", encoding="utf-8") as file:
                json.dump({"name": "test", "sizeBytes": size, "sha256": digest}, file)

        pin(7, hashlib.sha256(b"weights").hexdigest())
        self.assertEqual(providers.verify_model(model, manifest)["name"], "test")
        pin(8, hashlib.sha256(b"weights").hexdigest())
        with self.assertRaises(providers.Unavailable):
            providers.verify_model(model, manifest)
        pin(7, hashlib.sha256(b"Weights").hexdigest())
        with self.assertRaises(providers.Unavailable):
            providers.verify_model(model, manifest)
        with self.assertRaises(providers.Unavailable):
            providers.verify_model(os.path.join(directory, "absent.litertlm"), manifest)

    @unittest.skipIf(importlib.util.find_spec("mediapipe"), "MediaPipe is installed")
    def test_a_runtime_without_mediapipe_is_unavailable(self):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        model = os.path.join(directory, "model.litertlm")
        with open(model, "wb") as file:
            file.write(b"weights")
        manifest = os.path.join(directory, "manifest.json")
        with open(manifest, "w", encoding="utf-8") as file:
            json.dump({"name": "test", "sizeBytes": 7, "sha256": hashlib.sha256(b"weights").hexdigest()}, file)
        with self.assertRaises(providers.Unavailable):
            providers.MediaPipeProvider(model, manifest)


if __name__ == "__main__":
    unittest.main()
