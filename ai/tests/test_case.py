"""One case by one command (montage.case): the case file, the windows, and a whole run on made-up
footage with a stand-in embedding model. The run needs ffmpeg, ffprobe, NumPy and Pillow."""

import io
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

try:
    import numpy as np
    from PIL import Image

    from montage import case
except ImportError:
    np = case = None

HAS_FFMPEG = bool(shutil.which("ffmpeg") and shutil.which("ffprobe"))


class Embedder:
    """Stands in for EmbeddingGemma: a picture's vector is its 8×8 grey levels about their mean."""

    model = "stand-in"

    def __init__(self, *_args, **_kwargs):
        pass

    def embed_image(self, jpeg):
        with Image.open(io.BytesIO(jpeg)) as image:
            grey = np.asarray(image.convert("L").resize((8, 8)), dtype=np.float32).ravel()
        vector = grey - grey.mean() + 1e-3
        return vector / np.linalg.norm(vector)

    def embed_query(self, text):
        vector = np.random.default_rng(len(text)).normal(size=64).astype(np.float32)
        return vector / np.linalg.norm(vector)

    def close(self):
        pass


@unittest.skipIf(case is None, "needs NumPy and Pillow")
class CaseFileTest(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.work)

    def write(self, data):
        path = os.path.join(self.work, "case-01.json")
        with open(path, "w", encoding="utf-8") as file:
            json.dump(data, file)
        return path

    def test_sources_are_read_beside_the_case_in_order(self):
        read = case.read_case(self.write({"id": "case-01", "sources": [{"file": "a.mp4", "sha256": "AB"}, {"file": "b.mp4"}],
                                          "request": {"target_seconds": 60, "instruction": "孩子第一次骑车"}}))
        self.assertEqual((read["id"], read["target"], read["instruction"]), ("case-01", 60.0, "孩子第一次骑车"))
        self.assertEqual([(s["id"], s["path"]) for s in read["sources"]],
                         [("01", os.path.join(self.work, "a.mp4")), ("02", os.path.join(self.work, "b.mp4"))])

    def test_a_case_without_a_target_or_with_a_path_for_a_file_is_refused(self):
        for data in ({"id": "case-01", "sources": [{"file": "a.mp4"}], "request": {}},
                     {"id": "case-01", "sources": [{"file": "../a.mp4"}], "request": {"target_seconds": 30}},
                     {"id": "case-01", "sources": [], "request": {"target_seconds": 30}}):
            with self.subTest(data=data), self.assertRaises(case.CaseError):
                case.read_case(self.write(data))

    def test_windows_have_their_frame_in_the_middle_the_last_one_short(self):
        self.assertEqual(case.windows(5.5), [(0, 2, 1000), (2, 4, 3000), (4, 5.5, 4750)])

    def test_frames_keep_the_aspect_with_a_long_side_of_512(self):
        self.assertEqual(case.frame_size(3840, 2160), (512, 288))
        self.assertEqual(case.frame_size(1080, 1920), (288, 512))
        self.assertEqual(case.frame_size(320, 240), (320, 240))

    def test_cuts_closer_than_a_second_are_flashes(self):
        log = "\n".join(f"[Parsed_showinfo_3 @ 0x0] n:{n} pts:1 pts_time:{t} duration:1" for n, t in
                        enumerate([0.4, 3.0, 3.5, 7.25]))
        self.assertEqual(case.scene_cuts(log), [3.0, 7.25])


@unittest.skipUnless(case is not None and HAS_FFMPEG, "needs ffmpeg, ffprobe, NumPy and Pillow")
class CaseRunTest(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.work)
        self.cases, self.out = os.path.join(self.work, "case"), os.path.join(self.work, "out")
        os.makedirs(self.cases)
        # Three shots of 6 s with a tone, and a second source with no sound.
        sources = ["testsrc=d=6:s=640x360:r=25", "smptebars=d=6:s=640x360:r=25", "mandelbrot=s=640x360:r=25"]
        graph = "".join(f"[{k}:v]trim=duration=6,setpts=PTS-STARTPTS[v{k}];" for k in range(3))
        subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", *[a for s in sources for a in ("-f", "lavfi", "-i", s)],  # noqa: S607
                        "-f", "lavfi", "-i", "sine=f=440:d=18", "-filter_complex", graph + "[v0][v1][v2]concat=n=3:v=1[v]",
                        "-map", "[v]", "-map", "3:a", "-c:v", "libx264", "-c:a", "aac", "-shortest",
                        os.path.join(self.cases, "case-t-1.mp4")], check=True)
        subprocess.run(["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i",  # noqa: S607
                        "rgbtestsrc=d=5:s=360x640:r=25", "-c:v", "libx264", os.path.join(self.cases, "case-t-2.mp4")],
                       check=True)

    def write_case(self, sha_of_first=None):
        sources = [{"file": f, "sha256": case.sha256(os.path.join(self.cases, f))} for f in ("case-t-1.mp4", "case-t-2.mp4")]
        if sha_of_first:
            sources[0]["sha256"] = sha_of_first
        path = os.path.join(self.cases, "case-t.json")
        with open(path, "w", encoding="utf-8") as file:
            json.dump({"id": "case-t", "provider": "A", "sources": sources,
                       "request": {"target_seconds": 10, "format": {"music": False, "subtitles": False},
                                   "instruction": "彩色的测试画面"}}, file, ensure_ascii=False)
        return path

    def run_case(self, path):
        with mock.patch("anas_ai.providers.MediaPipeProvider", Embedder):
            return case.main([path, self.out, "--labels", ""])

    def test_a_case_is_cut_to_its_target_with_sound_and_every_step_timed(self):
        self.assertEqual(self.run_case(self.write_case()), 0)
        cut = os.path.join(self.out, "A-case-t.mp4")
        streams = json.loads(subprocess.run(
            ["ffprobe", "-v", "error", "-show_entries", "stream=codec_type:format=duration", "-of", "json", cut],  # noqa: S607
            capture_output=True, text=True, check=True).stdout)
        self.assertEqual(sorted(s["codec_type"] for s in streams["streams"]), ["audio", "video"])
        self.assertGreater(float(streams["format"]["duration"]), 10 - 1.6)
        with open(os.path.join(self.out, "pipeline.json"), encoding="utf-8") as file:
            pipeline = json.load(file)
        self.assertEqual((pipeline["case"], pipeline["result"], pipeline["cut"]), ("case-t", "成功", "A-case-t.mp4"))
        self.assertEqual([s["id"] for s in pipeline["steps"]],
                         ["sources", "decode", "embed", "labels", "cards", "plan", "fill", "render"])
        self.assertEqual(set(pipeline["categories"]), {"读取与解码", "理解", "切分", "选择与编排", "渲染"})
        decode = pipeline["steps"][1]
        self.assertEqual((decode["frames"], decode["shots"]), (9 + 3, 3 + 1))
        self.assertEqual(pipeline["external"]["count"], 0)
        with open(os.path.join(self.out, "plan.json"), encoding="utf-8") as file:
            plan = json.load(file)
        self.assertAlmostEqual(plan["seconds"], pipeline["cutSeconds"])
        self.assertTrue(all(s["end"] - s["start"] <= 6 for s in plan["segments"]))

    def test_a_source_that_does_not_match_its_sha256_fails_with_the_reason_recorded(self):
        self.assertEqual(self.run_case(self.write_case("0" * 64)), 1)
        with open(os.path.join(self.out, "pipeline.json"), encoding="utf-8") as file:
            pipeline = json.load(file)
        self.assertEqual(pipeline["result"], "失败")
        self.assertIn("sha256", pipeline["reason"])
        self.assertEqual(pipeline["steps"][0]["failed"], "CaseError")
        self.assertFalse(os.path.exists(os.path.join(self.out, "A-case-t.mp4")))


if __name__ == "__main__":
    unittest.main()
