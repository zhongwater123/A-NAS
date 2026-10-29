"""Cutting videos for the assistant (montage): FFmpeg commands, renders and plan checks.

Rendering tests need ffmpeg and ffprobe, and the shot-card tests NumPy; each is
skipped without them.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest

from montage import render

try:
    import numpy as np

    from montage import moments
except ImportError:
    np = moments = None

HAS_FFMPEG = bool(shutil.which("ffmpeg") and shutil.which("ffprobe"))


def still(path, width, height):
    subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607  test tool from PATH
                    f"color=c=teal:s={width}x{height}", "-frames:v", "1", path], check=True)


def clip(path, seconds, size="640x360"):
    subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607  test tool from PATH
                    f"testsrc=duration={seconds}:size={size}:rate=25", "-c:v", "libx264", path], check=True)


def probe(path):
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",  # noqa: S607  test tool from PATH
                          "stream=width,height,codec_name:format=duration", "-of", "json", path],
                         capture_output=True, text=True, check=True).stdout
    data = json.loads(out)
    return data["streams"][0], float(data["format"]["duration"])


class RenderTest(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.work)
        self.out = os.path.join(self.work, "out")

    def test_crossfade_keeps_the_total(self):
        args = render.ffmpeg_args(["a", "b", "c"], 2, True, "out.mp4")
        self.assertEqual(args[args.index("-t") + 1], "6")
        graph = args[args.index("-filter_complex") + 1]
        self.assertEqual([part.rsplit("trim=duration=", 1)[1] for part in graph.split(";")[:3]],
                         ["2.5[v0]", "2.5[v1]", "2[v2]"])
        self.assertIn("xfade=transition=fade:duration=0.5:offset=2[x1]", graph)
        self.assertIn("xfade=transition=fade:duration=0.5:offset=4[x2]", graph)

    def test_clips_seek_each_input(self):
        args = render.clip_args([("a.webm", 10, 12.5), ("b.mp4", 0, 3)], "out.mp4")
        self.assertEqual(args[args.index("a.webm") - 5:args.index("a.webm")], ["-ss", "10.000", "-t", "2.500", "-i"])
        self.assertIn("concat=n=2:v=1:a=0[x]", args[args.index("-filter_complex") + 1])

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg and ffprobe")
    def test_stills_render_landscape_and_portrait(self):
        images = [os.path.join(self.work, name) for name in ("w.jpg", "t.jpg", "w2.jpg")]
        for path, (w, h) in zip(images, [(1600, 900), (900, 1600), (1600, 900)], strict=True):
            still(path, w, h)
        for crossfade in (False, True):
            with self.subTest(crossfade=crossfade):
                stream, duration = probe(render.render(images, 1.5, crossfade, self.out))
                self.assertEqual((stream["codec_name"], stream["width"], stream["height"]), ("h264", 1920, 1080))
                self.assertAlmostEqual(duration, 4.5, delta=0.1)
        self.assertEqual(len(os.listdir(self.out)), 2)

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg and ffprobe")
    def test_clips_join_sources_of_different_sizes(self):
        wide, tall = os.path.join(self.work, "wide.mp4"), os.path.join(self.work, "tall.mp4")
        clip(wide, 6)
        clip(tall, 4, "360x640")
        stream, duration = probe(render.render_clips([(wide, 1, 3.5), (tall, 0.5, 2.5), (wide, 4, 5.5)], self.out))
        self.assertEqual((stream["width"], stream["height"]), (1920, 1080))
        self.assertAlmostEqual(duration, 6.0, delta=0.15)

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg and ffprobe")
    def test_a_failed_render_leaves_nothing(self):
        broken = os.path.join(self.work, "broken.jpg")
        with open(broken, "wb") as file:
            file.write(b"\xff\xd8 not really a jpeg")
        for make in (lambda: render.render([broken], 3, False, self.out),
                     lambda: render.render_clips([(broken, 0, 2)], self.out)):
            with self.assertRaises(render.RenderError):
                make()
        self.assertEqual(os.listdir(self.out), [])


@unittest.skipIf(np is None, "needs NumPy")
class MomentsTest(unittest.TestCase):
    """One 50 s video: shots at 0–8, 8–48 and 48–50 s, a window every 2 s."""

    def setUp(self):
        dims = 8
        basis = np.eye(dims, dtype=np.float32)
        windows, vectors = [], {}
        for k in range(25):
            start, middle = 2 * k, 2 * k + 1
            if middle < 8:
                vector = basis[0]  # a market
            elif middle < 28:
                vector = basis[1]  # a castle, the same picture for 20 s
            elif middle < 48:
                vector = basis[2] * 0.6 + basis[1] * 0.8  # the castle again, from another side
            else:
                vector = basis[3]
            frame = start * 1000 + 1000
            vectors[f"07/{frame}"] = vector / np.linalg.norm(vector)
            windows.append({"start": start, "end": start + 2, "frame": frame, "flat": k == 24,
                            "tags": ["城堡"] if 8 <= middle < 48 else ["市场"]})
        index = {"model": "test", "videos": {"07": {"file": "07.webm", "duration": 50.0,
                                                    "shots": [[0, 8], [8, 48], [48, 50]], "windows": windows}}}
        self.index = moments.Index(index, vectors)
        self.castle = basis[1]

    def test_long_shots_become_parts_and_alike_parts_share_a_group(self):
        cards = {c["id"]: c for c in self.index.cards}
        self.assertEqual(sorted(cards), ["07-0-0", "07-1-0", "07-1-1", "07-2-0"])
        self.assertEqual((cards["07-1-0"]["start"], cards["07-1-0"]["end"]), (8, 28))
        self.assertEqual(cards["07-1-1"]["shot"], [8, 48])
        self.assertNotEqual(cards["07-1-0"]["group"], cards["07-0-0"]["group"])

    def test_search_ranks_cards_and_leaves_out_flat_frames(self):
        found = self.index.search({"城堡": self.castle}, limit=10)
        ids = [c["id"] for c in found["cards"]]
        self.assertEqual(ids[0], "07-1-0")
        self.assertNotIn("07-2-0", ids)  # its only window is flat
        self.assertEqual(found["cards"][0]["tags"], ["城堡"])
        self.assertEqual(found["cards"][0]["probes"]["城堡"]["score"], 1.0)

    def test_a_good_plan_gives_the_clips(self):
        clips = self.index.check([{"card": "07-1-1", "start": 30, "end": 34}, {"card": "07-0-0", "start": 1, "end": 4}])
        self.assertEqual(clips, [("07", 30.0, 34.0), ("07", 1.0, 4.0)])

    def test_a_plan_breaking_the_rules_is_refused_with_every_problem(self):
        with self.assertRaises(moments.PlanError) as caught:
            self.index.check([
                {"card": "07-0-0", "start": 6, "end": 10},    # over the cut at 8 s
                {"card": "07-1-0", "start": 20, "end": 21},   # too short
                {"card": "07-1-1", "start": 20, "end": 24},   # same picture as the one before
                {"card": "nope", "start": 0, "end": 2},
            ])
        problems = caught.exception.problems
        self.assertEqual(len(problems), 4)
        for words in ("跨过镜头切点", "至少 1.5 秒", "同一段画面", "不是检索结果里的镜头卡片"):
            self.assertTrue(any(words in p for p in problems), words)

    def test_a_plan_with_no_segments_is_refused(self):
        with self.assertRaises(moments.PlanError):
            self.index.check([])


if __name__ == "__main__":
    unittest.main()
