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
from unittest import mock

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

    def test_the_last_part_of_a_montage_fades_to_black(self):
        graph = render.part_args("a.webm", 10, 13, True, "out.mkv", render.ENDING_FADE)
        graph = graph[graph.index("-filter_complex") + 1]
        self.assertIn("setpts=PTS-STARTPTS,fade=t=out:st=2.000:d=1.000[x]", graph)
        self.assertIn("afade=t=out:st=2.000:d=1.000", graph)
        short = render.part_args("a.webm", 10, 11.2, True, "out.mkv", render.ENDING_FADE)
        self.assertIn("fade=t=out:st=0.600:d=0.600[x]", short[short.index("-filter_complex") + 1])
        plain = render.part_args("a.webm", 10, 13, True, "out.mkv")
        self.assertNotIn("fade=t=out:st", plain[plain.index("-filter_complex") + 1].split(";")[0])

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

    def _pictures(self, letters, shots=None, flat=()):
        """An index of "cards": "picture" whose 2 s windows show the pictures letters name."""
        basis = dict(zip("ABCDEFGH", np.eye(8, dtype=np.float32), strict=True))
        windows = [{"start": 2 * k, "end": 2 * k + 2, "frame": 2000 * k + 1000, "flat": k in flat, "tags": []}
                   for k in range(len(letters))]
        meta = {"file": "08.webm", "duration": 2.0 * len(letters), "shots": shots or [[0, 2.0 * len(letters)]],
                "windows": windows}
        return moments.Index({"model": "test", "cards": "picture", "videos": {"08": meta}},
                             {f"08/{w['frame']}": basis[c] for c, w in zip(letters, windows, strict=True)})

    def test_picture_cards_keep_a_close_up_cut_into_a_shot_and_ignore_ffmpegs_cuts(self):
        # A A B A A | C C: B comes back to A within CARD_LOOK windows, so it stays in A's card;
        # the cut at 4 s that FFmpeg found inside A is not a card's edge (#416, 2026-11-08).
        index = self._pictures("AABAACC", shots=[[0, 4], [4, 14]])
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 10), (10, 14)])
        self.assertEqual([c["shot"] for c in index.cards], [[0, 10], [10, 14]])
        # A picture that does not come back within CARD_LOOK windows is a card of its own.
        index = self._pictures("AABBBBAA")
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 4), (4, 12), (12, 16)])

    def test_a_flat_window_goes_with_the_card_before_it(self):
        index = self._pictures("AAABBB", flat={3})
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 8), (8, 12)])

    def test_a_picture_card_past_card_long_is_cut_at_its_least_alike_windows(self):
        with mock.patch.object(moments, "CARD_LONG", 10.0), mock.patch.object(moments, "CARD_SIDE", 4.0):
            # One card by CARD_ALIKE (B comes back), 14 s past 10 s: cut where A meets B, 4 s a side.
            index = self._pictures("AAAAABA")
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 10), (10, 14)])

    def test_key_windows_take_each_change_and_a_long_run_every_key_gap(self):
        index = self._pictures("AABAACC")
        first = index.cards[0]  # A A B A A
        # A run is PIECE_MIN windows at least: B takes the A after it, the next A starts a run.
        self.assertEqual(index.key_windows(first), [0, 2, 4])
        # A run of one picture for 14 windows: its first, then one every KEY_GAP seconds.
        index = self._pictures("A" * 14)
        self.assertEqual([index.rows[i][1] for i in index.key_windows(index.cards[0])], [0, 12, 24])
        # Fewer than KEY_FEWEST: none (the caller spreads that many evenly).
        self.assertEqual(self._pictures("AAAA").key_windows(self._pictures("AAAA").cards[0]), [])
        # Past KEY_MOST: the most alike neighbouring pair gives up its later window.
        index = self._pictures("A" * 20)
        self.assertEqual(len(index.key_windows(index.cards[0])), 4)
        with mock.patch.object(moments, "KEY_MOST", 3):
            self.assertEqual(len(index.key_windows(index.cards[0])), 3)

    def test_shot_cards_are_as_before_without_the_picture_layout(self):
        index = self._pictures("AABAACC", shots=[[0, 4], [4, 14]])
        index_shots = moments.Index({"model": "test", "videos": index.videos | {"08": index.videos["08"] | {
            "shots": [[0, 4], [4, 14]]}}}, {f"08/{2000 * k + 1000}": index.frames[k] for k in range(7)})
        self.assertEqual(index_shots.layout, "shots")
        self.assertEqual([c["id"] for c in index_shots.cards][:2], ["08-0-0", "08-1-0"])

    def test_a_split_near_a_cut_joined_as_the_same_place_falls_on_that_cut(self):
        basis = np.eye(4, dtype=np.float32)
        windows = [{"start": 2 * k, "end": 2 * k + 2, "frame": 2000 * k + 1000, "flat": False, "tags": []}
                   for k in range(6)]
        # The picture changes from the window at 6 s; the index joined the cut at 5.3 s as one place.
        vectors = {f"08/{w['frame']}": basis[0] if k < 3 else basis[1] for k, w in enumerate(windows)}
        meta = {"file": "08.webm", "duration": 12.0, "shots": [[0, 12]], "windows": windows}
        index = moments.Index({"model": "test", "videos": {"08": meta}}, vectors)
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 6), (6, 12)])
        index = moments.Index({"model": "test", "videos": {"08": meta | {"same_place": [[5.3, 0.87]]}}}, vectors)
        self.assertEqual([(c["start"], c["end"]) for c in index.cards], [(0, 5.3), (5.3, 12)])

    def test_a_cards_lines_are_its_sentences_with_their_seconds_in_it(self):
        card = {"id": "07-1-0", "video": "07", "start": 10.0, "end": 20.0}
        self.index.said = {"07": [(8.0, 9.5, "Look"), (9.6, 10.4, " at"), (10.5, 11.0, " that."),
                                  (12.0, 12.5, "这是"), (12.5, 13.0, "外滩"),
                                  (19.0, 19.8, "And"), (19.9, 21.0, " then")]}
        self.assertEqual(self.index.lines(card), [(0, 1.0, "…at that."), (2.0, 3.0, "这是外滩"), (9.0, 10.0, "And then…")])
        self.assertEqual(self.index.said_line(card), "台词：0–1秒「…at that.」 2–3秒「这是外滩」 9–10秒「And then…」")
        self.assertEqual(self.index.said_line({"id": "07-1-1", "video": "07", "start": 30.0, "end": 40.0}), "")

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
        # Three frames in the middles of its thirds (8-28 s), not on its windows' frames.
        self.assertEqual(index.keyframes(castle, 3), [11333, 18000, 24667])
        self.assertEqual(index.keyframes({"start": 24.0, "end": 25.0}, 3), [24167, 24500, 24833])  # a short card

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
        self.assertEqual(self.index.catalog(times=False)["videos"]["07"]["cards"][1],
                         "07-1-0 晴天 山上的城堡（亮点：塔楼） 毛病：抖动")

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
        for words in ("跨过镜头切点", "至少 1.5 秒", "同一段画面", "不是目录或检索结果里的卡片"):
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

    def test_a_segment_runs_the_seconds_it_was_given_past_six(self):
        # 剪法23 (#416): no segment is held to CLIP_MAX once it is set.
        self.assertEqual(self.index.check([{"card": "07-1-1", "start": 30, "end": 38}]), [("07", 30.0, 38.0)])

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

    def test_fill_moves_an_end_inside_speech_to_the_nearest_pause(self):
        self.index.words = {"07": [(9.0, 9.6), (9.7, 10.4)]}
        spans, _problems = self.index.fill([("07-1-0", self.castle)], "probe", 2, self.at)
        self.assertEqual(spans, [(8, 10.55)])  # after the line: no pause inside it
        self.index.words = {"07": [(9.0, 9.4), (9.8, 10.5)]}
        spans, _problems = self.index.fill([("07-1-0", self.castle)], "probe", 2, self.at)
        self.assertEqual(spans, [(8, 9.6)])  # the pause in the line is closer than its end

    def test_fill_widens_an_end_inside_a_sentence_to_take_it_whole(self):
        # A sentence from 9 to 13 s: the end at 10 s widens past the 2 s the plan gave, and past
        # SNAP, to the sentence's end and SENTENCE_PAD after it (方案 B's whole_sentences).
        self.index.said = {"07": [(9.0, 9.5, "Look"), (9.6, 13.0, " there.")]}
        self.index.words = {"07": [(9.0, 9.5), (9.6, 13.0)]}
        spans, _problems = self.index.fill([("07-1-0", self.castle)], "probe", 2, self.at, caps=[2], shares=[2])
        self.assertEqual(spans, [(8, 13.2)])
        # A sentence that runs past the card's region stays cut, as in 方案 B: no pause near either.
        self.index.said = {"07": [(9.0, 9.5, "Look"), (9.6, 29.0, " there.")]}
        self.index.words = {"07": [(9.0, 9.5), (9.6, 29.0)]}
        spans, _problems = self.index.fill([("07-1-0", self.castle)], "probe", 2, self.at, caps=[2], shares=[2])
        self.assertEqual(spans, [(8, 10)])

    def test_topped_grows_a_segment_back_to_its_planned_seconds(self):
        region = (8, 28.0)
        # The end grows first, a tenth of a second at a time, to the seconds the plan gave it.
        self.assertEqual(self.index.topped("07", 10, 11.7, 2.4, region, []), (10, 12.4))
        # At the region's end the start grows instead.
        self.assertEqual(self.index.topped("07", 26.5, 28, 2.4, region, []), (25.6, 28))
        # Never into another segment: here neither end may grow.
        self.assertEqual(self.index.topped("07", 26.5, 28, 2.4, region, [["07", 25, 26.5]]), (26.5, 28))
        # An end the steps leave inside a word goes back to the word's start.
        self.index.words = {"07": [(12.1, 12.8)]}
        self.assertEqual(self.index.topped("07", 10, 11.7, 2.4, region, []), (10, 12.1))

    def test_fill_gives_each_segment_an_even_share_before_the_best_steps(self):
        # The market (0-8 s) is one still picture: every step scores as its peak. The castle's
        # other side (28-48 s) is still too, but its first side, before 28 s, falls to 0.8.
        market, other_side = np.eye(8, dtype=np.float32)[0], np.eye(8, dtype=np.float32)[2] * 0.6 + self.castle * 0.8
        spans, _problems = self.index.fill([("07-0-0", market), ("07-1-1", other_side)], "probe", 8, self.at)
        self.assertEqual([e - s for s, e in spans], [4, 4])

    def test_a_segment_keeps_to_its_card_and_to_frames_like_its_peak(self):
        cards = {c["id"]: c for c in self.index.cards}
        # The castle's two sides are 0.8 alike: each card is its own region of the shot.
        self.assertEqual(self.index.region(cards["07-1-0"]), (8, 28))
        self.assertEqual(self.index.region(cards["07-1-1"]), (28, 48))
        # A cut the detection missed at 31 s: what follows is another picture, never taken.
        other = np.eye(8, dtype=np.float32)[5]

        def at(video, seconds):
            return other if seconds > 31 else self.at(video, seconds)

        spans, _problems = self.index.fill([("07-1-1", self.castle)], "frame", 6, at)
        self.assertEqual(spans, [(28, 31)])

    def test_scenes_group_cards_by_their_places_and_bind_the_plan(self):
        self.index.scenes = {"07-0-0": "室外，集市", "07-1-0": "室外，城堡", "07-1-1": "室外，城堡的另一面"}
        basis = np.eye(4, dtype=np.float32)
        vectors = {"07-0-0": basis[0], "07-1-0": basis[1], "07-1-1": basis[1] * 0.95 + basis[2] * 0.31}
        scene_of, names = self.index.scene_groups(vectors)
        self.assertEqual(scene_of, {"07-0-0": 1, "07-1-0": 2, "07-1-1": 2})
        self.assertEqual(names[1], "室外，集市")
        self.index.scene_of, self.index.scene_names = scene_of, names
        catalog = self.index.catalog(times=False, scenes=True)
        self.assertTrue(catalog["scenes"]["2"].endswith("（2 张卡片）"))
        self.assertIn("场景2", catalog["videos"]["07"]["cards"][1])

        def plan(*cards):
            return [{"card": c, "start": s, "end": s + 2} for c, s in cards]

        # Back to a scene it left, and many segments of one scene: the plan's call.
        self.index.check(plan(("07-1-0", 10), ("07-0-0", 2), ("07-1-1", 30)))
        self.index.check(plan(("07-1-0", 8), ("07-1-0", 12), ("07-1-1", 30), ("07-1-1", 36)))

    def test_a_summary_that_loops_is_told_from_one_that_names_a_thing_thrice(self):
        from montage import look

        named = "（亮点：机械巨兽）" * 3
        self.assertFalse(look.looping("巨兽在废墟中移动" + named))
        self.assertTrue(look.looping("一名戴帽男子在黑暗中，手持手电筒，神情专注，" * 3))
        self.assertEqual(look.COPIED.sub("", "老人触摸机械（亮点：机器人与老人）。船舱（01-14-0、01-15-0）"), "老人触摸机械。船舱")
        self.assertEqual(look.COPIED.sub("", "火箭喷射火焰，亮点为喷射火焰；三张图展示男子做手势"), "火箭喷射火焰；男子做手势")

    def test_parts_open_where_colour_and_light_change_most_near_each_minute(self):
        starts = [0, 20, 40, 52, 58, 63, 70, 90, 110, 118, 125, 140]
        changes = [0, 0.1, 0.1, 0.2, 0.9, 0.3, 0.1, 0.1, 0.1, 0.8, 0.2, 0.1]
        # Near 60 s the card at 58 s changes most; near 118 s, the one at 118 s; then the film ends.
        self.assertEqual(moments.part_starts(starts, changes), [0, 4, 9])
        self.assertEqual(moments.part_starts([0, 25, 90], [0, 0.5, 0.5]), [0, 2])  # none near 60 s: the nearest
        self.assertEqual(moments.part_starts([0, 10], [0, 0.9]), [0])  # shorter than a part

    def test_a_part_gives_the_card_whose_picture_its_probe_matches_best(self):
        self.index.parts = [{"video": "07", "cards": ["07-0-0", "07-1-0", "07-1-1"], "summary": ""}]
        self.assertEqual(self.index.pick(1, self.castle), "07-1-0")
        self.assertEqual(self.index.pick(1, self.castle, {"07-1-0"}), "07-1-1")  # taken: the next best
        self.assertIsNone(self.index.pick(2, self.castle))
        self.index.parts.append({"video": "07", "cards": ["07-2-0"], "summary": ""})
        self.assertIsNone(self.index.pick(2, self.castle))  # its one window is flat: nothing to cut

    def test_planned_seconds_are_the_plans_own_at_least_the_minimum(self):
        self.assertEqual(moments.planned_seconds([3, 4, 2]), [3, 4, 2])
        self.assertEqual(moments.planned_seconds([30, 30]), [30, 30])  # no ceiling, no scaling (剪法23)
        self.assertEqual(moments.planned_seconds([1, 9]), [moments.SEGMENT_MIN, 9])

    def test_a_paragraph_runs_on_in_film_order_from_its_card(self):
        cards = [c["id"] for c in sorted(self.index.cards, key=lambda c: c["start"])]
        self.assertEqual(cards, ["07-0-0", "07-1-0", "07-1-1", "07-2-0"])
        self.assertEqual(self.index.run("07-0-0", 2), ["07-0-0", "07-1-0"])
        self.assertEqual(self.index.run("07-0-0", 2, {"07-1-0"}), ["07-0-0", "07-1-1"])  # taken: the next one
        # 07-2-0 has nothing to cut: the film ends first, and the paragraph reaches back.
        self.assertEqual(self.index.run("07-1-1", 2), ["07-1-0", "07-1-1"])
        self.index.parts = [{"video": "07", "cards": ["07-0-0", "07-1-0"], "summary": ""},
                            {"video": "07", "cards": ["07-1-1", "07-2-0"], "summary": ""}]
        self.assertEqual(self.index.run("07-1-0", 3), ["07-0-0", "07-1-0"])  # it keeps to its part

    def test_a_card_is_paced_by_how_much_its_picture_changes(self):
        self.index.pictures = {"07-0-0": {"change": 0.004}, "07-1-0": {"change": 0.03}, "07-1-1": {"change": 0.1}}
        self.assertEqual([self.index.pace_of(i) for i in ("07-0-0", "07-1-0", "07-1-1", "07-2-0")],
                         ["慢", "中", "快", None])
        self.index.pictures["07-2-0"] = {"change": 0.0}  # still, with its one window flat: slow however short
        self.assertEqual(self.index.pace_of("07-2-0"), "慢")

    def test_a_cut_ends_on_a_segment_aiming_at_middle_seconds_at_the_least(self):
        self.assertEqual(moments.aims(["慢", "快"]), [moments.PACES["慢"], moments.PACES["中"]])
        self.assertEqual(moments.aims(["快", "慢"]), [moments.PACES["快"], moments.PACES["慢"]])
        self.assertEqual(moments.aims([]), [])

    def test_colours_count_a_picture_s_colour_and_light(self):
        red, blue = np.tile([250, 10, 10], (40, 1)), np.tile([10, 10, 250], (40, 1))
        self.assertAlmostEqual(float(moments.colours(red) @ moments.colours(red)), 1, places=5)
        self.assertAlmostEqual(float(moments.colours(red) @ moments.colours(blue)), 0, places=5)
        half = moments.colours(np.concatenate([red, blue]))
        self.assertAlmostEqual(float(half @ moments.colours(red)), 0.5 ** 0.5, places=5)

    def test_a_plan_changes_scene_only_to_one_alike_enough(self):
        basis = np.eye(4, dtype=np.float32)
        self.index.scene_of = {"07-0-0": 1, "07-1-0": 2, "07-1-1": 3}
        self.index.scene_names = {1: "集市", 2: "城堡", 3: "地牢"}
        red, blue = moments.colours(np.tile([250, 10, 10], (4, 1))), moments.colours(np.tile([10, 10, 250], (4, 1)))
        alike = self.index.scene_likeness({"07-0-0": basis[0], "07-1-0": basis[0] + basis[1] * 0.5, "07-1-1": basis[2]},
                                          {"07-0-0": red, "07-1-0": red, "07-1-1": blue})
        self.assertEqual(alike, {1: {2: 1.0, 3: 0.25}, 2: {1: 1.0, 3: 0.25}, 3: {1: 0.25, 2: 0.25}})
        self.index.scene_alike = alike
        self.assertNotIn("可接", self.index.catalog(times=False, scenes=True)["scenes"]["1"])
        scenes = self.index.catalog(times=False, scenes=True, joins=True)["scenes"]
        self.assertTrue(scenes["1"].endswith("可接：2"))
        self.assertTrue(scenes["3"].endswith("可接：无（只能放开头或结尾）"))

        def plan(*cards):
            return [{"card": c, "start": s, "end": s + 2} for c, s in cards]

        self.index.check(plan(("07-0-0", 2), ("07-1-1", 30)))  # unlike scenes: the plan's call

    def test_title_scenes_are_left_out_of_the_likeness(self):
        self.index.scene_of = {"07-0-0": 1, "07-1-0": 2, "07-1-1": 3}
        self.index.texts = {"07-1-1": "片名：MERIDIAN"}
        colour = moments.colours(np.tile([250, 10, 10], (4, 1)))
        vectors = {i: np.eye(4, dtype=np.float32)[0] for i in self.index.scene_of}
        self.assertEqual(self.index.scene_likeness(vectors, dict.fromkeys(self.index.scene_of, colour)),
                         {1: {2: 1.0}, 2: {1: 1.0}})

    def test_a_card_says_the_most_seconds_a_segment_of_it_can_run(self):
        cards = {c["id"]: c for c in self.index.cards}
        self.assertEqual(self.index.usable(cards["07-1-0"]), 20)  # 20 s of one picture, all of it (剪法23)
        self.assertEqual(self.index.usable(cards["07-2-0"]), 0)  # its only window is flat
        self.assertIn("07-1-0 可剪20秒", self.index.catalog(times=False, lengths=True)["videos"]["07"]["cards"][1])

    def test_fill_grows_each_segment_to_its_own_share_first(self):
        # Paced slow and fast: the slow one takes 6 s first, though both score alike all along.
        market, other_side = np.eye(8, dtype=np.float32)[0], np.eye(8, dtype=np.float32)[2] * 0.6 + self.castle * 0.8
        spans, _problems = self.index.fill([("07-0-0", market), ("07-1-1", other_side)], "probe", 8, self.at,
                                           caps=[6, 2.5], mins=[4, 1.5], shares=[6, 2])
        self.assertEqual([e - s for s, e in spans], [6, 2])

    def test_fill_keeps_each_segment_to_its_minimum(self):
        market = np.eye(8, dtype=np.float32)[0]
        spans, _problems = self.index.fill([("07-0-0", market), ("07-1-0", self.castle)], "probe", 3, self.at,
                                           mins=[1.5, 2.5])
        self.assertEqual([e - s for s, e in spans], [1.5, 2.5])

    def test_fill_with_a_floor_keeps_a_segment_its_card_cannot_take_to_its_minimum(self):
        market = np.eye(8, dtype=np.float32)[0]
        lo, hi = self.index.region(next(c for c in self.index.cards if c["id"] == "07-0-0"))
        longer = hi - lo + 1
        spans, problems = self.index.fill([("07-0-0", market)], "probe", 5, self.at, caps=[longer], mins=[longer])
        self.assertEqual((spans, len(problems)), ([None], 1))
        spans, problems = self.index.fill([("07-0-0", market)], "probe", 5, self.at, caps=[longer], mins=[longer],
                                          floor=moments.SEGMENT_MIN)
        self.assertEqual((problems, spans), ([], [(lo, hi)]))

    def test_a_trimmed_plan_may_run_shorter_than_the_planned_minimum(self):
        segment = {"card": "07-1-1", "start": 30, "end": 31.3}
        with self.assertRaises(moments.PlanError):
            self.index.check([segment])
        self.assertEqual(self.index.check([segment], shortest=moments.TRIMMED_MIN), [("07", 30.0, 31.3)])

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

    def test_titles_are_text_laid_over_the_picture_of_a_title_kind(self):
        answers = {
            "招牌": ("LAPD", "实物", "品牌标志"),
            "片尾": ("Cast Old Thom", "叠加", "演职员表"),
            "界面": ("SIMULATION READY", "叠加", "仪器屏幕或界面上的字"),
            "空": ("无", "", ""),
        }

        class Reading(look.Vision):
            def ask(self, prompt, jpeg, max_tokens=120):
                read, laid, kind = answers[jpeg.decode()]
                return read if prompt == look.READ else laid if prompt.endswith("“实物”。") else kind

        vision = Reading("http://127.0.0.1:1/v1")
        self.assertEqual(vision.text(["空".encode(), "招牌".encode(), "片尾".encode()]), "演职员表：Cast Old Thom")
        self.assertEqual(vision.text(["界面".encode(), "招牌".encode()]), "")
        self.assertEqual([look.kind_of(a) for a in ("资助说明", "“片名”。", "不知道")], ["出品或资助说明", "片名", ""])

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
        self.assertEqual(measured["change"], 0)

    def test_the_picture_changes_more_the_faster_it_moves(self):
        slow = picture.measure(self.crops([(2 * k, 0) for k in range(10)]))["change"]
        fast = picture.measure(self.crops([(20 * k, 0) for k in range(10)]))["change"]
        self.assertGreater(slow, 0)
        self.assertGreater(fast, 2 * slow)

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
