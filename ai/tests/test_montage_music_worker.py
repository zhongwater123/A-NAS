"""The music analysis Worker (ai/anas_montage_music): it reads the two streams
the montage center decodes, answers the eighth version's loudness, and
refuses what is not a song. allin1 itself runs only in the music runtime
(scripts/build-montage-music-runtime.sh); here a stand-in answers for it, and
ai/eval/montage_music_allin1.py compares the real one with the development
machine's structure files.

NumPy is in the AI runtime only: make ai-test skips these, and the runtime's
image runs them (docs/development/ai-development.md)."""

import base64
import importlib.util
import json
import os
import socket
import struct
import threading
import unittest

RUNTIME = bool(importlib.util.find_spec("numpy"))

if RUNTIME:
    import numpy as np

    from anas_montage_music import worker


class StandIn:
    identity = "stand-in"

    def load(self):
        return None


def tone(seconds, rate, channels):
    t = np.arange(int(seconds * rate)) / rate
    y = (0.5 * np.sin(2 * np.pi * 440 * t) * np.minimum(1.0, t / 2)).astype(np.float32)
    return np.repeat(y[:, None], channels, axis=1)


@unittest.skipUnless(RUNTIME, "NumPy is in the AI runtime")
class WorkerTest(unittest.TestCase):
    def setUp(self):
        self.found = {"bpm": 120, "beats": [0.5, 1.0], "downbeats": [0.5], "segments": [{"start": 0, "end": 3, "label": "intro"}]}
        original = worker.structure
        worker.structure = lambda models, stereo: {**self.found, "frames": len(stereo)}
        self.addCleanup(setattr, worker, "structure", original)

    def call(self, request, streams):
        """Sends request with a pipe per stream, written while the Worker reads, as the montage center does."""
        ours, theirs = socket.socketpair()
        pipes = [os.pipe() for _ in streams]
        writers = [threading.Thread(target=self.write, args=(w, data), daemon=True) for (_, w), data in zip(pipes, streams, strict=True)]
        body = json.dumps(request).encode()
        socket.send_fds(ours, [struct.pack(">I", len(body)) + body], [r for r, _ in pipes])
        for r, _ in pipes:
            os.close(r)
        for w in writers:
            w.start()
        server = threading.Thread(target=worker.handle, args=(StandIn(), theirs), daemon=True)
        server.start()
        (length,) = struct.unpack(">I", ours.recv(4))
        data = b""
        while len(data) < length:
            data += ours.recv(length - len(data))
        server.join()
        ours.close()
        theirs.close()
        return json.loads(data)

    @staticmethod
    def write(fd, data):
        try:
            os.write(fd, data) if len(data) < 65536 else [os.write(fd, data[i:i + 65536]) for i in range(0, len(data), 65536)]
        except BrokenPipeError:
            pass
        finally:
            os.close(fd)

    def streams(self):
        return [{"name": "stereo", "channels": 2, "rate": 44100}, {"name": "mono", "channels": 1, "rate": 22050}]

    def test_a_song_is_analysed_with_the_eighth_versions_loudness(self):
        stereo, mono = tone(3, 44100, 2), tone(3, 22050, 1)
        answer = self.call({"op": "analyze", "streams": self.streams()}, [stereo.tobytes(), mono.tobytes()])
        self.assertTrue(answer["ok"], answer)
        self.assertEqual(answer["frames"], len(stereo))
        self.assertEqual(answer["model"], "stand-in")
        loudness = np.frombuffer(base64.b64decode(answer["loudness"]), "<f4")
        # librosa.feature.rms(y) with its defaults, as the eighth version computes it.
        y = mono[:, 0]
        padded = np.pad(y, 1024)
        n = 1 + (len(padded) - 2048) // 512
        expected = np.array([np.sqrt(np.mean(padded[k * 512:k * 512 + 2048] ** 2)) for k in range(n)], np.float32)
        np.testing.assert_allclose(loudness, expected, rtol=1e-5)
        self.assertEqual(len(loudness), 1 + len(y) // 512)

    def test_what_is_not_a_song_is_invalid_input(self):
        stereo, mono = tone(3, 44100, 2), tone(3, 22050, 1)
        cases = {
            "nothing": ([b"", mono.tobytes()], self.streams()),
            "a broken frame": ([stereo.tobytes()[:-4], mono.tobytes()], self.streams()),
            "other rates": ([stereo.tobytes(), mono.tobytes()], [{**s, "rate": 48000} for s in self.streams()]),
            "not numbers": ([np.full_like(stereo, np.nan).tobytes(), mono.tobytes()], self.streams()),
        }
        for name, (data, streams) in cases.items():
            with self.subTest(name):
                answer = self.call({"op": "analyze", "streams": streams}, data)
                self.assertFalse(answer["ok"])
                self.assertEqual(answer["error"]["code"], "invalid_input")

    def test_a_song_longer_than_the_montage_center_takes_is_refused(self):
        with self.assertRaises(worker.Refused):
            r, w = os.pipe()
            threading.Thread(target=self.write, args=(w, tone(3, 100, 2).tobytes()), daemon=True).start()
            try:
                worker.read_stream(r, 2, seconds=2, rate=100)
            finally:
                os.close(r)

    def test_the_manifest_digests_are_checked(self):
        import tempfile

        folder = tempfile.mkdtemp()
        with open(os.path.join(folder, "a.pth"), "wb") as f:
            f.write(b"weights")
        manifest = os.path.join(folder, "m.json")
        with open(manifest, "w") as f:
            json.dump({"parts": [{"file": "a.pth", "sha256": "0" * 64}]}, f)
        with self.assertRaises(RuntimeError):
            worker.checked_files(manifest, folder)
        with open(manifest, "w") as f:
            json.dump({"file": "a.pth", "sha256": worker.digest(os.path.join(folder, "a.pth"))}, f)
        self.assertEqual(list(worker.checked_files(manifest, folder)[1]), ["a.pth"])


if __name__ == "__main__":
    unittest.main()
