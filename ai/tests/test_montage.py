"""Cutting videos for the assistant (montage): FFmpeg commands, renders and plan checks.

Rendering and frame tests need ffmpeg and ffprobe, the shot-card tests NumPy and the
picture measures OpenCV; each is skipped without them. The vision model is a stand-in that answers by rule.
"""

import json
import os
import shutil
import subprocess
import tempfile
import unittest

from montage import look, render

try:
    import numpy as np

    from montage import moments
except ImportError:
    np = moments = None

try:
    import cv2

    from montage import picture
except ImportError:
    cv2 = picture = None

HAS_FFMPEG = bool(shutil.which("ffmpeg") and shutil.which("ffprobe"))


def still(path, width, height):
    subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607  test tool from PATH
                    f"color=c=teal:s={width}x{height}", "-frames:v", "1", path], check=True)


def clip(path, seconds, size="640x360"):
    subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607  test tool from PATH
                    f"testsrc=duration={seconds}:size={size}:rate=25", "-c:v", "libx264", path], check=True)


def sounding(path, seconds, start):
    """A clip with mono 44.1 kHz sound that is silent until start, then a tone."""
    subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607  test tool from PATH
                    f"testsrc=duration={seconds}:size=640x360:rate=25", "-f", "lavfi", "-i",
                    f"aevalsrc='if(gte(t,{start}),0.5*sin(2*PI*440*t),0)':s=44100:d={seconds}",
                    "-c:v", "libx264", "-c:a", "aac", "-shortest", path], check=True)


def sound_of(path):
    """The audio stream's rate, channels and duration, and where its first silence ends."""
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "a:0", "-show_entries",  # noqa: S607  test tool from PATH
                          "stream=sample_rate,channels,duration", "-of", "json", path],
                         capture_output=True, text=True, check=True).stdout
    stream = json.loads(out)["streams"][0]
    found = subprocess.run(["ffmpeg", "-nostdin", "-i", path, "-af", "silencedetect=noise=-40dB:d=0.1",  # noqa: S607  test tool from PATH
                            "-f", "null", "-"], capture_output=True, text=True, check=True).stderr
    ends = [float(line.split("silence_end: ")[1].split()[0]) for line in found.splitlines() if "silence_end" in line]
    return int(stream["sample_rate"]), stream["channels"], float(stream["duration"]), ends


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

    def test_a_part_seeks_its_clip_and_keeps_its_sound_as_pcm(self):
        args = render.part_args("a.webm", 10, 12.5, True, "out.mkv")
        self.assertEqual(args[args.index("a.webm") - 5:args.index("a.webm")], ["-ss", "10.000", "-t", "2.500", "-i"])
        graph = args[args.index("-filter_complex") + 1]
        self.assertIn("[0:a]aresample=48000", graph)
        self.assertIn("afade=t=out:st=2.470:d=0.030", graph)
        self.assertEqual(args[args.index("-c:a") + 1], "pcm_s16le")

    def test_a_part_without_sound_gets_silence_of_its_length(self):
        graph = render.part_args("b.mp4", 0, 3, False, "out.mkv")
        graph = graph[graph.index("-filter_complex") + 1].split(";")[1]
        self.assertTrue(graph.startswith("anullsrc=r=48000:cl=stereo,"))
        self.assertIn("atrim=duration=3.000", graph)

    def test_parts_are_joined_with_the_picture_copied(self):
        args = render.join_args("parts.txt", "out.mp4")
        self.assertEqual(args[args.index("-c:v") + 1], "copy")
        self.assertEqual(args[args.index("-c:a") + 1], "aac")

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
    def test_clips_keep_their_own_sound_aligned_and_silence_where_there_is_none(self):
        quiet, tone = os.path.join(self.work, "quiet.mp4"), os.path.join(self.work, "tone.mp4")
        clip(quiet, 3)
        sounding(tone, 6, 2)
        # The tone starts 2 s into its source: 0.5 s into the second clip, 1.5 s into the cut.
        cut = render.render_clips([(quiet, 0, 1), (tone, 1.5, 3.5)], self.out)
        rate, channels, seconds, ends = sound_of(cut)
        self.assertEqual((rate, channels), (48000, 2))
        self.assertAlmostEqual(seconds, probe(cut)[1], delta=0.1)
        self.assertAlmostEqual(ends[0], 1.5, delta=0.05)

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

    def test_a_shot_is_cut_where_its_picture_changes_and_alike_parts_share_a_group(self):
        cards = {c["id"]: c for c in self.index.cards}
        self.assertEqual(sorted(cards), ["07-0-0", "07-1-0", "07-1-1", "07-2-0"])
        # The castle shot turns to another side at 28 s, not at its middle.
        self.assertEqual((cards["07-1-0"]["start"], cards["07-1-0"]["end"]), (8, 28))
        self.assertEqual((cards["07-1-1"]["start"], cards["07-1-1"]["end"]), (28, 48))
        self.assertEqual(cards["07-1-1"]["shot"], [8, 48])
        self.assertNotEqual(cards["07-1-0"]["group"], cards["07-0-0"]["group"])

    def test_a_long_picture_that_holds_is_cut_into_equal_parts(self):
        vector = np.eye(4, dtype=np.float32)[0]
        windows = [{"start": 2 * k, "end": 2 * k + 2, "frame": 2000 * k + 1000, "flat": False, "tags": []}
                   for k in range(25)]
        index = moments.Index({"model": "test", "videos": {"08": {"file": "08.webm", "duration": 50.0,
                                                                  "shots": [[0, 50]], "windows": windows}}},
                              {f"08/{w['frame']}": vector for w in windows})
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 16.67), (16.67, 33.33), (33.33, 50)])

    def write_index(self, work):
        with open(os.path.join(work, "index.json"), "w", encoding="utf-8") as file:
            json.dump({"model": "test", "videos": self.index.videos}, file)
        keys = [f"07/{w['frame']}" for w in self.index.videos["07"]["windows"]]
        np.savez(os.path.join(work, "frames.npz"), keys=np.array(keys), vectors=self.index.frames)

    def test_captions_beside_the_index_ride_on_the_cards_they_were_written_for(self):
        work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, work)
        self.write_index(work)
        with open(os.path.join(work, moments.CAPTIONS), "w", encoding="utf-8") as file:
            json.dump({"cards": {"07-1-0": {"start": 8, "end": 28, "caption": "山上的城堡"},
                                 "07-1-1": {"start": 8, "end": 48, "caption": "cut another way"}}}, file)
        index = moments.Index.load(os.path.join(work, "index.json"), os.path.join(work, "frames.npz"))
        self.assertEqual(index.captions, {"07-1-0": "山上的城堡"})
        found = {c["id"]: c for c in index.search({"城堡": self.castle})["cards"]}
        self.assertEqual(found["07-1-0"]["caption"], "山上的城堡")
        self.assertNotIn("caption", found["07-1-1"])
        castle = next(c for c in index.cards if c["id"] == "07-1-0")
        # Three frames spread over its ten windows (8-28 s): the first, the fifth, the last.
        self.assertEqual([index.frame_ms[i] for i in index.keyframes(castle, 3)], [9000, 17000, 27000])

    def test_cards_carry_light_highlight_motion_and_flaws_when_written(self):
        with tempfile.TemporaryDirectory() as work:
            self.write_index(work)
            with open(os.path.join(work, moments.CAPTIONS), "w", encoding="utf-8") as file:
                json.dump({"cards": {"07-1-0": {"start": 8, "end": 28, "caption": "山上的城堡",
                                                "light": "晴天", "highlight": "城堡塔楼"}}}, file)
            looks = {"motion": "向前走", "sharp": 900.0, "shake": 0.02, "bright": 120.0, "clipped": 0.4}
            with open(os.path.join(work, moments.PICTURES), "w", encoding="utf-8") as file:
                json.dump({"cards": {"07-1-0": {"start": 8, "end": 28, **looks},
                                     "07-1-1": {"start": 8, "end": 48, **looks}}}, file)
            index = moments.Index.load(os.path.join(work, "index.json"), os.path.join(work, "frames.npz"))
        found = {c["id"]: c for c in index.search({"城堡": self.castle})["cards"]}
        castle = found["07-1-0"]
        self.assertEqual((castle["light"], castle["highlight"], castle["motion"]), ("晴天", "城堡塔楼", "向前走"))
        self.assertEqual(castle["flaws"], ["抖动", "过曝"])
        self.assertNotIn("motion", found["07-1-1"])  # measured over other seconds

    def test_a_condition_narrows_the_cards_before_the_probe_ranks_them(self):
        self.index.captions = {"07-0-0": "雨中的市场", "07-1-0": "山上的城堡", "07-1-1": "城堡的另一面"}
        self.index.notes = {"07-0-0": {"light": "夜晚", "highlight": "摊位"},
                            "07-1-0": {"light": "晴天", "highlight": "塔楼"},
                            "07-1-1": {"light": "夜晚", "highlight": "雨中的城墙"}}
        def ids(**kw):
            return [c["id"] for c in self.index.search({"城堡": self.castle}, **kw)["cards"]]

        # The castle by night is its other side, though the sunny one scores higher.
        self.assertEqual(ids(lights=["夜晚"]), ["07-1-1", "07-0-0"])
        # A word in the caption or in the highlight.
        self.assertEqual(ids(words=["雨"]), ["07-1-1", "07-0-0"])
        self.assertEqual(ids(lights=["晴天"], words=["雨"]), [])
        self.assertEqual(self.index.search({"城堡": self.castle}, each=1, lights=["夜晚"])["each"], {"城堡": ["07-1-1"]})

    def test_the_catalog_lists_every_card_in_time_order_as_a_line(self):
        self.index.captions = {"07-1-0": "山上的城堡"}
        self.index.notes = {"07-1-0": {"light": "晴天", "highlight": "塔楼"}}
        catalog = self.index.catalog()["videos"]["07"]
        self.assertEqual(catalog["seconds"], 50.0)
        self.index.pictures = {"07-1-0": {"start": 8, "end": 28, "motion": "固定", "sharp": 900.0, "shake": 0.02,
                                          "bright": 120.0, "clipped": 0.0}}
        self.assertEqual(catalog["cards"][:1], ["07-0-0 0–8 秒（镜头 0–8） （没有描述）"])
        self.assertEqual(self.index.catalog()["videos"]["07"]["cards"][1],
                         "07-1-0 8–28 秒（镜头 8–48） 晴天 山上的城堡（亮点：塔楼） 毛病：抖动")
        self.assertEqual(self.index.catalog(["08"]), {"videos": {}})

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

    def test_cards_left_out_each_say_why_once(self):
        plan = [{"card": "07-1-0", "start": 10, "end": 14}]
        self.assertEqual(self.index.check(plan, skipped=[{"card": "07-0-0", "why": "市场，不是城堡"}]), [("07", 10.0, 14.0)])
        self.assertEqual(self.index.check(plan, skipped=[]), [("07", 10.0, 14.0)])
        with self.assertRaises(moments.PlanError) as caught:
            self.index.check(plan, skipped=[{"card": "07-1-0", "why": "用过了"}, {"card": "nope", "why": "x"},
                                            {"card": "07-0-0", "why": ""}, {"card": "07-0-0", "why": "又一次"}])
        problems = caught.exception.problems
        for words in ("已经用在计划里", "不是检索结果里的镜头卡片", "why 要用一句话", "出现了不止一次"):
            self.assertTrue(any(words in p for p in problems), words)

    def test_a_segment_over_six_seconds_is_refused_changing_or_not(self):
        with self.assertRaises(moments.PlanError) as caught:
            self.index.check([{"card": "07-1-1", "start": 30, "end": 38}])
        self.assertIn("最多 6 秒", caught.exception.problems[0])
        self.assertEqual(self.index.check([{"card": "07-1-1", "start": 30, "end": 36.004}]), [("07", 30.0, 36.004)])

    def test_a_plan_over_a_flat_frame_or_with_a_long_reason_is_refused(self):
        with self.assertRaises(moments.PlanError) as caught:
            self.index.check([{"card": "07-2-0", "start": 48, "end": 50},
                              {"card": "07-0-0", "start": 1, "end": 4, "reason": "长" * 201}])
        self.assertEqual(len(caught.exception.problems), 2)
        self.assertIn("纯黑或纯色", caught.exception.problems[0])

    def test_search_each_keeps_every_probe_its_own_best_groups(self):
        # The market scores below both castle parts, so the best two overall leave it out;
        # each probe's own best keeps it.
        probes = {"城堡": self.castle, "市场": (np.eye(8)[0] + np.eye(8)[4]) / np.sqrt(2)}
        self.assertEqual([c["id"] for c in self.index.search(probes, limit=2)["cards"]], ["07-1-0", "07-1-1"])
        found = self.index.search(probes, each=1)
        self.assertEqual(found["each"], {"城堡": ["07-1-0"], "市场": ["07-0-0"]})
        self.assertEqual([c["id"] for c in found["cards"]], ["07-1-0", "07-0-0"])
        # A part that looks like one already kept gives its place to the next picture.
        for card in self.index.cards:
            if card["id"] == "07-1-1":
                card["group"] = next(c["group"] for c in self.index.cards if c["id"] == "07-1-0")
        self.assertEqual(self.index.search({"城堡": self.castle}, each=2)["each"]["城堡"], ["07-1-0", "07-0-0"])

    def test_screening_keeps_what_the_vision_model_passed_its_best_first(self):
        probes = {"城堡": self.castle, "市场": (np.eye(8)[0] + np.eye(8)[4]) / np.sqrt(2)}
        found = self.index.search(probes, each=3)
        verdicts = {(i, probe): {"passes": True, "score": 6, "seen": probe}
                    for probe, ids in found["each"].items() for i in ids}
        verdicts[("07-1-0", "城堡")] = {"passes": False, "score": 2, "seen": "台阶"}
        verdicts[("07-0-0", "城堡")] = {"passes": True, "score": 9, "seen": "城堡"}
        kept = moments.screened(found, verdicts, 2)
        self.assertEqual(kept["each"]["城堡"], ["07-0-0", *[i for i in found["each"]["城堡"] if i not in ("07-1-0", "07-0-0")][:1]])
        self.assertEqual(kept["refused"]["城堡"], 1)
        self.assertNotIn("07-1-0", kept["each"]["城堡"])
        card = next(c for c in kept["cards"] if c["id"] == "07-0-0")
        self.assertEqual(card["screen"]["城堡"], {"score": 9, "seen": "城堡"})
        self.assertNotIn("screen", next(c for c in found["cards"] if c["id"] == "07-0-0"))

    def test_search_keeps_to_the_videos_given(self):
        self.assertEqual(self.index.search({"城堡": self.castle}, videos=["08"])["cards"], [])

    def test_rules_take_a_still_picture_briefly_and_never_fill_the_target(self):
        segments = self.index.plan({"城堡": self.castle}, 60)
        # Two pictures of the castle shot pass the threshold, each the same for 20 s:
        # CLIP_MAX of each from its best window, and nothing more however long the target.
        self.assertEqual([(s["card"], s["start"], s["end"]) for s in segments],
                         [("07-1-0", 8, 14), ("07-1-1", 28, 34)])
        self.assertEqual(self.index.check(segments), [("07", 8.0, 14.0), ("07", 28.0, 34.0)])
        self.assertEqual(self.index.plan({"城堡": -self.castle}, 10), [])

    def test_a_span_grows_from_the_best_window_while_the_probe_holds(self):
        other_side = np.eye(8, dtype=np.float32)[2] * 0.6 + self.castle * 0.8
        # 26-28 s, before the card, is the first side of the castle: 0.8 against its 1.0.
        self.assertEqual(self.index.span("07-1-1", other_side, "probe"), (28, 34))
        # The windows already in the plan are left out, the best of the card's own included.
        self.assertEqual(self.index.span("07-1-1", other_side, "probe", [("07", 27.5, 32)]), (32, 38))
        self.assertIsNone(self.index.span("07-0-0", self.castle, "frame", [("07", 0, 8)]))
        self.assertIsNone(self.index.span("07-2-0", self.castle, "probe"))  # its only window is flat

    def test_a_span_by_the_picture_holds_where_the_probe_score_falls(self):
        # A pan: each window turns 0.3 rad further from the first, 0.955 alike to the one before.
        basis = np.eye(4, dtype=np.float32)
        windows, vectors = [], {}
        for k in range(5):
            windows.append({"start": 2 * k, "end": 2 * k + 2, "frame": 2000 * k + 1000, "flat": False, "tags": []})
            vectors[f"10/{2000 * k + 1000}"] = basis[0] * np.cos(0.3 * k) + basis[1] * np.sin(0.3 * k)
        index = moments.Index({"model": "test", "videos": {"10": {"file": "10.webm", "duration": 10.0,
                                                                  "shots": [[0, 10]], "windows": windows}}}, vectors)
        card = index.cards[0]["id"]
        self.assertEqual(index.span(card, basis[0], "probe"), (0, 2))
        self.assertEqual(index.span(card, basis[0], "frame"), (0, 4))

    def test_a_span_cut_short_by_its_shot_takes_its_neighbours_or_none(self):
        basis = np.eye(8, dtype=np.float32)
        windows = [{"start": 2 * k, "end": 2 * k + 2, "frame": 2000 * k + 1000, "flat": False, "tags": []}
                   for k in range(5)]
        index = moments.Index({"model": "test", "videos": {"10": {
            "file": "10.webm", "duration": 10.0, "shots": [[0, 1.2], [1.2, 6.8], [6.8, 10]], "windows": windows}}},
            {f"10/{w['frame']}": basis[k] for k, w in enumerate(windows)})
        # 6.8-8 is all the matched window has in its shot; the next joins though it scores 0.
        self.assertEqual(index.span("10-2-0", basis[3], "probe"), (6.8, 10))
        self.assertIsNone(index.span("10-0-0", basis[0], "frame"))  # a shot of 1.2 s

    def at(self, _video, seconds):
        """The frame at seconds, as the index has it every 2 s."""
        return self.index.frames[int(seconds // 2)]

    def test_fill_grows_the_best_ends_to_the_target_within_each_cap(self):
        market = np.eye(8, dtype=np.float32)[0]
        spans, problems = self.index.fill([("07-0-0", market), ("07-1-0", self.castle)], "probe", 8, self.at,
                                          caps=[6, 3])
        self.assertEqual(problems, [])
        self.assertEqual(spans[1][1] - spans[1][0], 3)  # its cap
        self.assertEqual(spans[0], (0, 5))  # from its best window, 0-2 s, to the target
        # Short of the target when every segment is at its cap.
        spans, _problems = self.index.fill([("07-0-0", market), ("07-1-0", self.castle)], "probe", 20, self.at,
                                           caps=[6, 3])
        self.assertEqual(sum(e - s for s, e in spans), 9)
        # Over the target, the worst end steps go, down to 1.5 s a segment.
        spans, _problems = self.index.fill([("07-0-0", market), ("07-1-0", self.castle)], "probe", 3, self.at)
        self.assertEqual([e - s for s, e in spans], [1.5, 1.5])

    def test_fill_by_picture_keeps_to_the_peak_picture(self):
        other_side = np.eye(8, dtype=np.float32)[2] * 0.6 + self.castle * 0.8
        # 26-28 s is the first side of the castle, 0.8 alike to the peak at 28-30 s: the end that
        # grows is the later one, up to CLIP_MAX.
        spans, _problems = self.index.fill([("07-1-1", other_side)], "frame", 30, self.at)
        self.assertEqual(spans, [(28, 34)])

    def test_fill_moves_an_end_inside_speech_to_the_next_pause(self):
        self.index.words = {"07": [(9.0, 9.6), (9.7, 10.4)]}
        spans, _problems = self.index.fill([("07-1-0", self.castle)], "probe", 2, self.at)
        self.assertEqual(spans, [(8, 10.55)])

    def test_search_keeps_to_windows_meeting_a_condition(self):
        # The castle seen from the other side (28-48 s) leans to basis 2; asking for that
        # rather than basis 1 leaves out the first castle part.
        found = self.index.search({"城堡": self.castle}, when=[(np.eye(8)[2], np.eye(8)[1] * 0.7)])
        self.assertEqual([c["id"] for c in found["cards"]], ["07-1-1"])

    def test_seconds_rounded_to_two_places_stay_inside_a_cut_at_three(self):
        index = moments.Index({"model": "test", "videos": {"09": {"file": "09.webm", "duration": 6.0,
                                                                  "shots": [[0, 2.004], [2.004, 6.0]],
                                                                  "windows": self.index.videos["07"]["windows"][:3]}}},
                              {f"09/{w['frame']}": self.index.frames[i] for i, w in
                               enumerate(self.index.videos["07"]["windows"][:3])})
        self.assertEqual(index.check([{"card": "09-1-0", "start": 2.0, "end": 6.0}]), [("09", 2.0, 6.0)])
        with self.assertRaises(moments.PlanError):
            index.check([{"card": "09-1-0", "start": 1.99, "end": 6.0}])

    def test_a_plan_with_no_segments_is_refused(self):
        with self.assertRaises(moments.PlanError):
            self.index.check([])

    def test_a_plan_to_be_looked_at_says_what_each_segment_shows(self):
        segment = {"card": "07-1-1", "start": 30, "end": 34}
        self.assertEqual(self.index.check([segment]), [("07", 30.0, 34.0)])
        with self.assertRaises(moments.PlanError) as caught:
            self.index.check([segment], shows_needed=True)
        self.assertIn("shows", caught.exception.problems[0])
        self.assertEqual(self.index.check([segment | {"shows": "城堡"}], shows_needed=True), [("07", 30.0, 34.0)])

    def test_rules_pass_over_a_card_the_frames_refuse_and_take_the_run_that_fits(self):
        asked = []

        def looking(card, start, end, probe):
            asked.append((card["id"], start, end, probe))
            return None if card["id"] == "07-1-0" else (start + 1, end)

        segments = self.index.plan({"城堡": self.castle}, 60, look=looking)
        self.assertEqual(asked, [("07-1-0", 8, 14, "城堡"), ("07-1-1", 28, 34, "城堡")])
        self.assertEqual([(s["card"], s["start"], s["end"], s["shows"]) for s in segments], [("07-1-1", 29, 34, "城堡")])


class Seeing:
    """A vision model that sees what the frame's bytes name."""

    def __init__(self, sees):
        self.sees = sees

    def fits(self, jpeg, shows):
        return jpeg.decode() == self.sees, jpeg.decode()


class LookTest(unittest.TestCase):
    def test_the_longest_run_allows_one_slip(self):
        T, F = True, False
        self.assertEqual(look.run([F, T, T, F, T, T, F, F, T]), (1, 5))
        self.assertEqual(look.run([T, F, F, T, T]), (3, 4))
        self.assertIsNone(look.run([F, F]))

    def test_a_screening_answer_passes_when_it_is_there_and_scores_enough(self):
        self.assertEqual(look.screened("有，8 分，城堡立在山上。"),
                         {"fits": True, "score": 8, "seen": "城堡立在山上。", "passes": True})
        self.assertFalse(look.screened("有，3分，远处有一点城墙")["passes"])
        self.assertFalse(look.screened("没有，0 分，是一条街道")["passes"])
        unscored = look.screened("有。画面里是城堡")
        self.assertEqual((unscored["score"], unscored["passes"], unscored["seen"]), (None, True, "画面里是城堡"))

    def review(self, shown):
        frames = [(10 + 0.5 * k + 0.25, word.encode()) for k, word in enumerate(shown)]
        original = look.frames
        look.frames = lambda *_args, **_kw: frames
        self.addCleanup(setattr, look, "frames", original)
        return look.review(Seeing("船"), "v.mp4", 10, 10 + 0.5 * len(shown), "湖上的小船")

    def test_a_segment_is_trimmed_to_the_frames_that_show_it(self):
        seen = self.review(["天鹅", "船", "船", "船", "船", "船", "天鹅", "天鹅"])
        self.assertEqual((seen["start"], seen["end"], seen["fit"], seen["frames"]), (10.5, 13.0, 5, 8))
        self.assertTrue(seen["usable"])
        self.assertFalse(seen["fits"])
        self.assertEqual(seen["seen"], "船")

    def test_a_segment_with_no_run_long_enough_is_not_usable(self):
        seen = self.review(["天鹅", "船", "天鹅", "天鹅", "船", "船", "天鹅", "天鹅"])
        self.assertFalse(seen["usable"])
        self.assertEqual(seen["seen"], "天鹅")

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg and ffprobe")
    def test_frames_are_taken_every_half_second_from_the_segment(self):
        work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, work)
        path = os.path.join(work, "clip.mp4")
        clip(path, 6)
        frames = look.frames(path, 1, 4)
        self.assertEqual([t for t, _jpeg in frames], [1.25, 1.75, 2.25, 2.75, 3.25, 3.75])
        self.assertTrue(all(jpeg.startswith(b"\xff\xd8") for _t, jpeg in frames))


if __name__ == "__main__":
    unittest.main()


@unittest.skipIf(cv2 is None, "needs OpenCV")
class PictureTest(unittest.TestCase):
    """Frames cut out of one textured picture, as a camera would see it."""

    def setUp(self):
        noise = np.random.default_rng(7).integers(0, 256, (600, 900), dtype=np.uint8)
        self.scene = cv2.GaussianBlur(noise, (0, 0), 2)

    def crops(self, offsets, scales=None):
        frames = []
        for k, (x, y) in enumerate(offsets):
            scale = scales[k] if scales else 1.0
            w, h = round(480 / scale), round(270 / scale)
            cx, cy = 450 + x, 300 + y
            crop = self.scene[cy - h // 2:cy - h // 2 + h, cx - w // 2:cx - w // 2 + w]
            frames.append(cv2.resize(crop, (480, 270), interpolation=cv2.INTER_LINEAR))
        return np.stack(frames)

    def test_a_still_camera_is_fixed_and_steady(self):
        measured = picture.measure(self.crops([(0, 0)] * 10))
        self.assertEqual(measured["motion"], "固定")
        self.assertLess(measured["shake"], 0.0005)

    def test_the_picture_moving_right_is_the_camera_turning_left(self):
        # 10 px of 480 every 0.2 s is a fifth of the width a second.
        self.assertEqual(picture.measure(self.crops([(-10 * k, 0) for k in range(10)]))["motion"], "向左转")
        self.assertEqual(picture.measure(self.crops([(10 * k, 0) for k in range(10)]))["motion"], "向右转")

    def test_a_growing_picture_is_the_camera_walking_on(self):
        measured = picture.measure(self.crops([(0, 0)] * 10, [1.02**k for k in range(10)]))
        self.assertEqual(measured["motion"], "向前走")
        self.assertAlmostEqual(measured["zoom"], np.log(1.02) / picture.STEP, delta=0.02)

    def test_jitter_about_a_steady_path_is_shake(self):
        jitter = np.random.default_rng(3).integers(-4, 5, (20, 2))
        shaky = picture.measure(self.crops([tuple(j) for j in jitter]))
        self.assertEqual(shaky["motion"], "固定")
        self.assertGreater(shaky["shake"], 0.003)

    def test_blur_and_exposure(self):
        sharp = picture.measure(self.crops([(0, 0)] * 3))
        blurred = picture.measure(np.stack([cv2.GaussianBlur(f, (0, 0), 3) for f in self.crops([(0, 0)] * 3)]))
        self.assertLess(blurred["sharp"], sharp["sharp"] / 4)
        white = picture.measure(np.full((3, 270, 480), 255, np.uint8))
        self.assertEqual((white["bright"], white["clipped"]), (255.0, 1.0))

    @unittest.skipUnless(HAS_FFMPEG, "needs ffmpeg")
    def test_frames_are_decoded_grey_at_the_step(self):
        with tempfile.TemporaryDirectory() as work:
            video = os.path.join(work, "pan.mp4")
            subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607  test tool from PATH
                            "testsrc2=s=640x360:r=25:d=3", "-pix_fmt", "yuv420p", video], check=True)
            shape = picture.size(video)
            self.assertEqual(shape, (480, 270))
            grey = picture.frames(video, 1.0, 2.0, shape)
            self.assertEqual(grey.shape, (5, 270, 480))
