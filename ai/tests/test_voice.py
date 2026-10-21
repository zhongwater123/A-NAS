"""Protocol tests of the voice Worker with the fake speaker model (standard library only)."""

import base64
import json
import math
import os
import shutil
import socket
import struct
import tempfile
import threading
import unittest

from anas_ai import voice


def call(path, request, samples=None):
    """Sends one request the way internal/voiceworker does and returns the response."""
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
        conn.connect(path)
        payload = json.dumps(request).encode()
        frame = struct.pack(">I", len(payload)) + payload
        if samples is None:
            conn.sendall(frame)
        else:
            fd = os.memfd_create("speech")
            try:
                os.write(fd, struct.pack(f"<{len(samples)}h", *samples))
                socket.send_fds(conn, [frame], [fd])
            finally:
                os.close(fd)
        header = b""
        while len(header) < 4:
            header += conn.recv(4 - len(header))
        (length,) = struct.unpack(">I", header)
        body = b""
        while len(body) < length:
            body += conn.recv(length - len(body))
        return json.loads(body)


def vector(encoded):
    data = base64.b64decode(encoded)
    return struct.unpack(f"<{len(data) // 4}f", data)


def cosine(a, b):
    return sum(x * y for x, y in zip(a, b))


SECOND = voice.SAMPLE_RATE


class VoiceWorkerTest(unittest.TestCase):
    def start(self, provider, reason=None):
        directory = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, directory, True)
        path = os.path.join(directory, "voice.sock")
        listener = voice.listening_socket(path)
        self.addCleanup(listener.close)
        thread = threading.Thread(target=voice.serve, args=(listener, provider, reason, 30), daemon=True)
        thread.start()
        self.addCleanup(self.stop, path, thread)
        return path, thread

    @staticmethod
    def stop(path, thread):
        """Asks the Worker to exit before its listener closes, as systemd's would."""
        if thread.is_alive():
            call(path, {"op": "release", "what": "all"})
            thread.join(5)

    def speech(self):
        """Two voices: two seconds of one, then two of the other, twice."""
        return [1000] * 2 * SECOND + [-2000] * 2 * SECOND + [1000] * 2 * SECOND + [-2000] * 2 * SECOND

    def test_info(self):
        path, _ = self.start(voice.FakeSpeakers())
        answer = call(path, {"op": "info"})
        self.assertEqual(answer, {"ok": True, "model": "fake-speakers-sha256", "dimensions": 32, "sampleRate": 16000})

    def test_embed_speakers_averages_each_group(self):
        path, _ = self.start(voice.FakeSpeakers())
        groups = [[[0, 2 * SECOND], [4 * SECOND, 2 * SECOND]], [[2 * SECOND, 2 * SECOND], [6 * SECOND, SECOND]]]
        answer = call(path, {"op": "embed_speakers", "groups": groups}, self.speech())
        self.assertTrue(answer["ok"], answer)
        first, second = (vector(value) for value in answer["vectors"])
        self.assertAlmostEqual(math.sqrt(sum(x * x for x in first)), 1, places=5)
        single = vector(call(path, {"op": "embed_speakers", "groups": [[[0, SECOND]]]}, self.speech())["vectors"][0])
        self.assertGreater(cosine(first, single), 0.98)
        self.assertLess(cosine(first, second), 0.9)

    def test_cluster_speech_labels_segments_by_voice(self):
        path, _ = self.start(voice.FakeSpeakers())
        segments = [[i * 2 * SECOND, 2 * SECOND] for i in range(4)]
        answer = call(path, {"op": "cluster_speech", "segments": segments, "threshold": 0.3}, self.speech())
        self.assertTrue(answer["ok"], answer)
        self.assertEqual(answer["labels"], [0, 1, 0, 1])
        self.assertEqual(len(answer["centroids"]), 2)

    def test_rejects_what_it_cannot_use(self):
        path, _ = self.start(voice.FakeSpeakers())
        speech = self.speech()
        for request, samples in [
            ({"op": "embed_speakers", "groups": [[[0, SECOND]]]}, None),
            ({"op": "embed_speakers", "groups": []}, speech),
            ({"op": "embed_speakers", "groups": [[[0, 100]]]}, speech),
            ({"op": "embed_speakers", "groups": [[[len(speech) - SECOND, 2 * SECOND]]]}, speech),
            ({"op": "embed_speakers", "groups": [[[-1, SECOND]]]}, speech),
            ({"op": "embed_speakers", "groups": [[[0, True]]]}, speech),
            ({"op": "cluster_speech", "segments": [[0, SECOND]], "threshold": 0}, speech),
        ]:
            with self.subTest(request=request):
                answer = call(path, request, samples)
                self.assertFalse(answer["ok"])
                self.assertEqual(answer["error"]["code"], "invalid_input")
        self.assertEqual(call(path, {"op": "speak"})["error"]["code"], "internal")

    def test_unavailable_model_answers_but_still_releases(self):
        path, thread = self.start(None, reason="model file missing")
        self.assertEqual(call(path, {"op": "info"})["error"], {"code": "unavailable", "message": "model file missing"})
        self.assertTrue(call(path, {"op": "release", "what": "all"})["ok"])
        thread.join(5)
        self.assertFalse(thread.is_alive())

    def test_segments_must_be_pairs_of_whole_samples(self):
        with self.assertRaises(Exception):
            voice.segments_of([[0.5, SECOND]], 2 * SECOND)
        self.assertEqual(voice.segments_of([[0, SECOND]], SECOND), [(0, SECOND)])


if __name__ == "__main__":
    unittest.main()
