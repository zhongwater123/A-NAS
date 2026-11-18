"""The compute Worker's music operation (ai/anas_montage/music_ops.py): the
eighth version's excerpt and slots, the loudest section when no chorus is
found, a start the member dragged, and the four reasons a song cannot be used
(docs/specs/montage-center.md, 配乐).

NumPy is in the AI runtime only: make ai-test skips these, and the runtime's
image runs them (docs/development/ai-development.md)."""

import importlib.util
import json
import os
import socket
import struct
import tempfile
import threading
import unittest

RUNTIME = bool(importlib.util.find_spec("numpy") and importlib.util.find_spec("PIL"))

if RUNTIME:
    import numpy as np

    from anas_montage import music_ops, worker
    from anas_montage.domain import music


def song(bar=2.0, bars=100, layout=None):
    """A structure with a downbeat every bar seconds, and its loudness: the choruses 6 dB louder."""
    downbeats = [round(bar * k, 3) for k in range(bars + 1)]
    beats = [round(bar / 4 * k, 3) for k in range(4 * bars + 1)]
    layout = layout or [("start", 0, 0), ("intro", 0, 8), ("verse", 8, 24), ("chorus", 24, 40), ("verse", 40, 56),
                        ("chorus", 56, 72), ("bridge", 72, 84), ("chorus", 84, 96), ("outro", 96, bars), ("end", bars, bars)]
    segments = [{"start": bar * a, "end": bar * b, "label": label} for label, a, b in layout]
    seconds = bar * bars
    times = np.arange(int(seconds * 22050 / 512) + 1) * 512 / 22050
    rms = np.full(len(times), 0.1, np.float32)
    for g in segments:
        if g["label"] == "chorus":
            rms[(times >= g["start"]) & (times < g["end"])] = 0.2
    return {"bpm": round(240 / bar), "beats": beats, "downbeats": downbeats, "segments": segments}, rms, times


def ask(st, rms, start=None, target=90.0):
    return music_ops.op_music({"structure": st, "target": target, "start": start, "song": "测试歌"}, {"loudness": rms})


@unittest.skipUnless(RUNTIME, "NumPy is in the AI runtime")
class MusicTest(unittest.TestCase):
    def test_the_default_excerpt_is_the_eighth_versions(self):
        st, rms, times = song()
        answer = ask(st, rms)
        expected = music.from_structure({**st, "song": "测试歌"}, rms, times, 90.0)
        self.assertEqual({k: v for k, v in answer.items() if k != "chorus"}, expected)
        self.assertTrue(answer["chorus"])
        self.assertNotIn("unusable", answer)

    def test_the_steps_copied_from_the_domain_give_its_parts(self):
        # sections() and parts() are from_structure's own steps, which a
        # dragged start needs on their own; with from_structure's start and
        # end they give its parts.
        st, rms, times = song()
        st = {**st, "song": ""}
        expected = music.from_structure(st, rms, times, 90.0)
        segs, _ = music_ops.sections(st, rms, times)
        start, end = expected["excerpt"]
        i = next(k for k, g in enumerate(segs) if g["end"] > start)
        j = max(k for k, g in enumerate(segs) if g["start"] < end)
        self.assertEqual(music_ops.parts(segs, i, j, start, end, st["downbeats"]), expected["parts"])

    def test_no_chorus_takes_the_loudest_section_and_asks_to_listen(self):
        layout = [("intro", 0, 8), ("verse", 8, 24), ("bridge", 24, 40), ("verse", 40, 56), ("inst", 56, 72), ("outro", 72, 100)]
        st, rms, times = song(layout=layout)
        rms[(times >= 80) & (times < 112)] = 0.3     # verse 40–56 bars: 80–112 s
        answer = ask(st, rms)
        self.assertFalse(answer["chorus"])
        self.assertNotIn("unusable", answer)
        climax = [p for p in answer["parts"] if p["level"] == "高潮"]
        self.assertEqual(len(climax), 1)
        self.assertEqual(climax[0]["start"] + answer["excerpt"][0], 80.0)

    def test_a_dragged_start_snaps_to_a_downbeat_and_lasts_the_target(self):
        st, rms, _ = song()
        answer = ask(st, rms, start=20.7)
        self.assertEqual(answer["excerpt"], [20.0, 110.0])
        self.assertEqual(answer["seconds"], 90.0)
        self.assertTrue(answer["chorus"])
        slots = [s for p in answer["parts"] for s in p["slots"]]
        self.assertEqual(slots[0][0], 0.0)
        self.assertEqual(slots[-1][1], 90.0)
        # Every cut is on a bar line of the song.
        for a, b in slots:
            self.assertAlmostEqual((a + 20.0) / 2.0, round((a + 20.0) / 2.0))
            self.assertAlmostEqual((b + 20.0) / 2.0, round((b + 20.0) / 2.0))

    def test_a_start_near_the_end_ends_with_the_music(self):
        st, rms, _ = song()
        answer = ask(st, rms, start=150.0)
        self.assertEqual(answer["excerpt"], [150.0, 200.0])
        self.assertEqual(answer["seconds"], 50.0)
        self.assertNotIn("unusable", answer)

    def test_a_start_leaving_too_little_cannot_be_used(self):
        st, rms, _ = song()
        self.assertEqual(ask(st, rms, start=180.0)["unusable"], "no_excerpt")

    def test_fewer_than_sixteen_bars_cannot_be_used(self):
        st, rms, _ = song(bars=15, layout=[("intro", 0, 4), ("chorus", 4, 12), ("outro", 12, 15)])
        self.assertEqual(ask(st, rms)["unusable"], "few_bars")

    def test_no_excerpt_of_thirty_seconds_cannot_be_used(self):
        st, rms, _ = song(bar=1.0, bars=25, layout=[("intro", 0, 4), ("chorus", 4, 20), ("outro", 20, 25)])
        self.assertEqual(ask(st, rms)["unusable"], "no_excerpt")

    def test_a_slot_longer_than_a_segment_cannot_be_used(self):
        # Two bars of 7 seconds: a quiet section's slot is 14 seconds.
        st, rms, _ = song(bar=7.0, bars=40, layout=[("intro", 0, 6), ("chorus", 6, 14), ("verse", 14, 30), ("outro", 30, 40)])
        self.assertEqual(ask(st, rms)["unusable"], "long_slot")

    def test_a_structure_that_is_not_allin1s_is_refused(self):
        st, rms, _ = song()
        for broken in ({**st, "downbeats": [3.0, 1.0]}, {**st, "segments": [{"start": 0, "end": 1, "label": "drop"}]},
                       {**st, "segments": []}, "structure"):
            with self.subTest(broken=str(broken)[:40]), self.assertRaises(ValueError):
                ask(broken, rms)
        with self.assertRaises(ValueError):
            ask(st, np.full(10, np.nan, np.float32))


@unittest.skipUnless(RUNTIME, "NumPy is in the AI runtime")
class ProtocolTest(unittest.TestCase):
    """The montage center sends the loudness in a memfd, as every compute Worker array."""

    def setUp(self):
        self.addCleanup(worker.OPS.pop, "music", None)
        worker.OPS["music"] = music_ops.op_music
        self.path = os.path.join(tempfile.mkdtemp(), "compute.sock")
        listener = worker.listening_socket(self.path)
        self.addCleanup(listener.close)
        threading.Thread(target=self.serve_one, args=(listener,), daemon=True).start()

    @staticmethod
    def serve_one(listener):
        conn, _ = listener.accept()
        with conn:
            worker.handle(conn)

    def call(self, request, rms):
        fd = os.memfd_create("test-loudness")
        os.write(fd, rms.astype("<f4").tobytes())
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as conn:
                conn.connect(self.path)
                body = json.dumps(request).encode()
                socket.send_fds(conn, [struct.pack(">I", len(body)) + body], [fd])
                (length,) = struct.unpack(">I", conn.recv(4))
                data = b""
                while len(data) < length:
                    data += conn.recv(length - len(data))
                return json.loads(data)
        finally:
            os.close(fd)

    def request(self, st, n, **more):
        return {"op": "music", "arrays": [{"name": "loudness", "dtype": "float32", "shape": [n]}], "structure": st,
                "target": 90, "start": None, **more}

    def test_the_excerpt_comes_over_the_socket(self):
        st, rms, times = song()
        answer = self.call(self.request(st, len(rms)), rms)
        self.assertTrue(answer["ok"], answer)
        self.assertEqual(answer["excerpt"], music.from_structure({**st, "song": ""}, rms, times, 90.0)["excerpt"])

    def test_a_broken_structure_is_invalid_input(self):
        st, rms, _ = song()
        answer = self.call(self.request({**st, "downbeats": "many"}, len(rms)), rms)
        self.assertFalse(answer["ok"])
        self.assertEqual(answer["error"]["code"], "invalid_input")


if __name__ == "__main__":
    unittest.main()
