"""第八版剪辑（montage_v8）：分层的约束、领域规则和配置。不需要视频、模型或歌曲，numpy 之外只用标准库。

2026-11-16 和第六版逐个镜头比对过（ai/montage_v8/README.md），之后第六版已删除。
"""

import ast
import sys
import unittest
from pathlib import Path

import numpy as np

from montage_v8 import config
from montage_v8.adapters import planner, vision
from montage_v8.domain import cards, catalog, describe, fill, music, picture, plan, quality
from montage_v8.domain.describe import DescribeError
from montage_v8.domain.options import Case, CutOptions
from montage_v8.domain.prompts import ORDER_ASKS, PICK

DOMAIN = Path(__file__).resolve().parents[1] / "montage_v8" / "domain"
SONG = {"song": "/songs/a.mp3", "seconds": 24.0, "parts": [
    {"start": 0.0, "end": 8.0, "level": "中等", "slots": [[0.0, 4.0], [4.0, 8.0]]},
    {"start": 8.0, "end": 24.0, "level": "高潮", "slots": [[8.0, 10.0], [10.0, 12.0], [12.0, 14.0], [14.0, 16.0], [16.0, 18.0]]}]}
CASE = Case(key="t", index="test", instruction="按拍摄时间顺序剪一条旅行 vlog", style="旅行", name="测试")


class LayerTest(unittest.TestCase):
    def test_the_domain_imports_only_the_standard_library_numpy_and_itself(self):
        io = {"subprocess", "os", "shutil", "socket", "requests", "PIL", "anthropic", "urllib", "http", "tempfile"}
        allowed = set(sys.stdlib_module_names) - io | {"numpy"}
        for path in DOMAIN.glob("*.py"):
            for node in ast.walk(ast.parse(path.read_text(encoding="utf-8"))):
                if isinstance(node, ast.Import):
                    names = [a.name.split(".")[0] for a in node.names]
                elif isinstance(node, ast.ImportFrom) and not node.level:
                    names = [node.module.split(".")[0]]
                else:
                    continue
                for name in names:
                    self.assertIn(name, allowed, f"{path.name} 引用了 {name}")


class QualityTest(unittest.TestCase):
    def test_a_dark_card_with_few_sharp_points_is_out_of_focus(self):
        self.assertTrue(quality.out_of_focus([22.0, 27.0, 27.0]))          # 新西兰 ne-070 的软星
        self.assertFalse(quality.out_of_focus([635.0, 723.0, 754.0]))      # 石头教堂上的星空
        self.assertFalse(quality.out_of_focus([None, None, 22.0]))         # 多数不暗：不判
        self.assertFalse(quality.out_of_focus([]))

    def test_blur_counts_only_while_the_camera_moves_and_the_reviewed_stay(self):
        still_dusk = {"blur": 9.0, "shake": 0.5, "blocked": 0.0}
        moving_blur = {"blur": 9.0, "shake": 2.0, "blocked": 0.0}
        finger = {"blur": 3.0, "shake": 0.5, "blocked": 0.3}
        got = quality.poor({"a": still_dusk, "b": moving_blur, "c": finger}, kept={"c"})
        self.assertEqual(got, {"b": ["模糊"]})


class CatalogTest(unittest.TestCase):
    def test_left_out_spans_poor_and_upright_cards_are_not_in_the_catalog(self):
        def card(cid, video, start, frame="w"):
            return {"id": cid, "video": video, "start": start, "end": start + 5, "frames": [frame]}

        described = [card("a", "001", 0), card("b", "001", 10), card("c", "002", 0, "u"), card("d", "002", 10)]
        got = catalog.usable(described, {"d": {"blur": float("nan"), "shake": 0, "blocked": 0}}, set(),
                             [{"video": "001", "from": 9, "to": 16}], wide=True, upright=lambda c: c["frames"][0] == "u")
        self.assertEqual([c["id"] for c in got], ["a"])

    def test_a_cards_mark_is_its_best_beats(self):
        labels = {"x-001/1": "1", "x-001/2": "3", "x-001/3": "3", "x-002/1": "x", "x-002/2": "x"}
        self.assertEqual(catalog.card_mark(labels, "x-001", 3), "精彩（2）")
        self.assertEqual(catalog.card_mark(labels, "x-002", 2), "有问题")
        self.assertEqual(catalog.card_mark(labels, "x-003", 2), "")


class MusicTest(unittest.TestCase):
    def test_a_parts_slots_are_shared_among_its_pictures_the_earlier_taking_the_odd_ones(self):
        got = music.slotted(SONG, [{"scenes": [{}]}, {"scenes": [{}, {}]}])
        self.assertEqual([len(p) for p in got[0]], [2])
        self.assertEqual([len(p) for p in got[1]], [3, 2])

    def test_step_one_reads_each_part_with_its_level_and_shots(self):
        note = music.music_note(SONG)
        self.assertIn("按音乐分成 2 段", note)
        self.assertIn("第 2 段 8–24 秒，高潮，能放 5 个镜头（每个约 2.0 秒）", note)

    def test_a_chorus_followed_by_a_louder_section_is_the_build_before_it(self):
        bars = [float(t) for t in range(0, 64, 2)]
        st = {"song": "/s.mp3", "bpm": 120, "downbeats": bars, "segments": [
            {"start": 0.0, "end": 16.0, "label": "verse"}, {"start": 16.0, "end": 32.0, "label": "chorus"},
            {"start": 32.0, "end": 48.0, "label": "chorus"}, {"start": 48.0, "end": 62.0, "label": "outro"}]}
        times = np.arange(0, 62, 0.1)
        rms = np.where((times >= 32) & (times < 48), 1.0, 0.5)
        got = music.from_structure(st, rms, times, 40.0)
        self.assertEqual([p["label"] for p in got["parts"]][:3], ["verse", "build", "chorus"][: len(got["parts"])])
        chorus = next(p for p in got["parts"] if p["level"] == "高潮")
        self.assertTrue(all(b - a == 2.0 for a, b in chorus["slots"]))      # 副歌每小节一个镜头


class PlanTest(unittest.TestCase):
    def test_a_beat_runs_from_its_key_frame_at_least_shortest_at_most_cap(self):
        card = {"start": 10.0, "end": 30.0, "times": [11.0, 12.0, 29.5],
                "answer": {"beats": [{"end": 12.0}, {"end": 29.5}, {"end": 30.0}]}}
        got = plan.units(card, plan.CAP)
        self.assertEqual(got, [(1, 10.0, 2.0), (2, 12.0, plan.CAP), (3, 28.0, 2.0)])

    def test_every_shooting_order_ask_is_in_step_ones_prompt(self):
        text = PICK + music.music_note(SONG)
        for ask, _instead in ORDER_ASKS:
            self.assertIn(ask, text)

    def test_free_order_drops_the_shooting_order_from_the_request(self):
        card = {"id": "t-001", "video": "001", "start": 0.0, "end": 6.0, "times": [1.0],
                "answer": {"process": ["走"], "scene": "湖边", "beats": [{"shot_size": "远景", "what": "湖", "end": 6.0}], "summary": "湖"}}
        _s, timed, _ = plan.pick_request(CASE, CutOptions(), [card], SONG)
        _s, free, schema = plan.pick_request(CASE, CutOptions(free_order=True), [card], SONG)
        self.assertIn("按拍摄时间顺序剪", timed)
        self.assertNotIn("按拍摄时间先后", free)       # 目录本身仍按拍摄时间排列
        self.assertIn("同一地点的画面连续排列", free)
        self.assertEqual(schema["properties"]["parts"]["minItems"], 2)

    def test_step_two_names_each_picture_and_turns_the_answer_back_into_lists(self):
        card = {"id": "t-001", "video": "001", "start": 0.0, "end": 6.0, "times": [1.0],
                "answer": {"process": ["走"], "scene": "湖边", "beats": [{"shot_size": "远景", "what": "湖", "end": 6.0}], "summary": "湖"}}
        picked = {"design": "d", "parts": [{"name": "一", "summary": "s", "link": "l", "scenes": [{"query": "q", "cards": ["t-001"]}]},
                                           {"name": "二", "summary": "s", "link": "l", "scenes": [{"query": "q2", "cards": ["t-001"]}]}]}
        req = plan.shots_request(CASE, CutOptions(), picked, {"t-001": card}, SONG)
        self.assertEqual(req["pictures"], [1, 1])
        answer = {"unmet": [], "parts": {"段落1": {"画面1": {"beats": ["t-001/1"], "shows": "s", "link": "开场"}},
                                         "段落2": {"画面1": {"beats": ["t-001/1"], "shows": "s2", "link": "l"}}}}
        raw, shaped = plan.shots_answer(answer, req["pictures"], picked)
        self.assertEqual(shaped["parts"][1]["pictures"][0]["shows"], "s2")
        self.assertEqual(raw["parts"][0]["pictures"][0]["link"], "开场")


class RunTest(unittest.TestCase):
    def test_one_video_gives_at_most_three_shots_in_a_row_two_of_them_small(self):
        kart = [("054", "远景"), ("054", "中景"), ("054", "近景")]       # pj-058 到 pj-060，一次滑车
        self.assertTrue(fill.breaks_run(kart, "054", "远景", set(), False))
        self.assertFalse(fill.breaks_run(kart, "056", "中景", set(), False))
        self.assertTrue(fill.breaks_run(kart[1:], "054", "特写", set(), False))
        self.assertFalse(fill.breaks_run(kart[1:], "054", "全景", set(), False))
        self.assertFalse(fill.breaks_run([], "054", "特写", set(), False))

    def test_with_marks_a_video_without_a_best_beat_runs_two(self):
        two = [("054", "远景"), ("054", "全景")]
        self.assertTrue(fill.breaks_run(two, "054", "远景", set(), True))
        self.assertFalse(fill.breaks_run(two, "054", "远景", {"054"}, True))


class FillTest(unittest.TestCase):
    def setUp(self):
        rows = [("001", float(t), float(t + 2)) for t in range(0, 20, 2)]
        vecs = np.eye(len(rows), 4)[:, :4] + 0.1
        self.ix = cards.WindowIndex({"001": {"file": "001.mov"}}, rows, vecs / np.linalg.norm(vecs, axis=1, keepdims=True),
                                    np.zeros(len(rows), bool))
        beat = {"shot_size": "远景", "what": "湖"}
        self.cards = {"t-001": {"id": "t-001", "video": "001", "start": 0.0, "end": 10.0, "times": [1.0, 6.0],
                                "frames": ["a.jpg", "b.jpg"], "answer": {"beats": [beat, {**beat, "shot_size": "中景"}]}}}

    def footage(self):
        return fill.Footage(self.cards, ["t-001"], self.ix, lambda v: f"/v/{v}.mov", set())

    def test_each_slot_is_filled_from_its_beats_key_frame_a_third_into_the_shot(self):
        m = {"parts": [{"level": "中等", "slots": [[0.0, 3.0], [3.0, 6.0]]}]}
        planned = {"slots": [[[[0.0, 3.0], [3.0, 6.0]]]],
                   "answer": {"parts": [{"name": "一", "pictures": [{"beats": ["t-001/2", "t-001/1"], "shows": "s", "link": "l"}]}]}}
        picked = {"parts": [{"scenes": [{"query": "q", "cards": ["t-001"]}]}]}
        clips, short = fill.fill(m, planned, picked, self.footage(), lambda *a: (a[3], a[4]), fps=30)
        self.assertEqual(short, [])
        self.assertEqual([c["beat"] for c in clips], ["t-001/1", "t-001/2"])   # 结尾画面：选的第一个节拍收尾
        self.assertAlmostEqual(fill.seconds(clips), 6.0)
        self.assertAlmostEqual(clips[1]["start"], 5.0)                       # 关键帧 6 秒在 3 秒镜头的三分之一处


class PictureTest(unittest.TestCase):
    def test_something_soft_coming_up_from_the_bottom_ends_the_clear_span(self):
        sharp = (np.indices((144, 256)).sum(axis=0) % 2 * 255).astype(np.float32)
        blocked = sharp.copy()
        blocked[72:, :] = 128.0                       # 下半幅是一片失焦的东西

        def frames(a, b):
            if b is None:
                return np.stack([sharp])
            return np.stack([blocked if a + i * picture.STEP >= 3.0 else sharp for i in range(int((b - a) / picture.STEP) + 1)])

        self.assertEqual(picture.clear(frames, 1.0, 0.0, 5.0), (0.0, 2.75))

    def test_a_steady_pan_is_not_shake(self):
        base = np.random.default_rng(0).random((180, 320)).astype(np.float32) * 255
        pan = np.stack([np.roll(base, 2 * i, axis=1) for i in range(12)])
        self.assertLess(picture.jitter(pan), 0.5)


class DescribeTest(unittest.TestCase):
    def test_one_beat_a_key_frame_with_its_frames_after_the_answer(self):
        content, schema, spans = describe.request(["a.jpg", "b.jpg"], [0.0, 4.0], [1, 2], 8.0)
        self.assertEqual([k for k, _ in content].count("image"), 2)
        self.assertEqual(schema["properties"]["beats"]["minItems"], 2)
        answer = describe.check({"beats": [{"shot_size": "远景", "what": "a"}, {"shot_size": "中景", "what": "b"}]}, spans)
        self.assertEqual(answer["beats"][1]["frames"], [2, 2])
        with self.assertRaises(DescribeError):
            describe.check({"beats": [{"shot_size": "远景", "what": "a"}]}, spans)

    def test_a_prompt_mode_answer_is_checked_locally(self):
        _content, schema, _spans = describe.request(["a.jpg"], [0.0], [1], 4.0)
        good = {"beats": [{"shot_size": "远景", "what": "湖"}], "process": ["看湖"], "summary": "湖", "scene": "湖边"}
        vision._required(vision.parse_json("```json\n" + __import__("json").dumps(good) + "\n```"), schema)
        with self.assertRaises(DescribeError):
            vision._required({**good, "beats": [{"shot_size": "大景", "what": "湖"}]}, schema)
        with self.assertRaises(DescribeError):
            vision._required({k: v for k, v in good.items() if k != "scene"}, schema)


class ConfigTest(unittest.TestCase):
    def test_every_profile_loads_and_the_board_decodes_and_encodes_on_the_vpu(self):
        for name in ("mac-dev", "x86-nas", "rk3588"):
            config.load(name)
        board = config.load("rk3588")
        self.assertIn("rkmpp", board.ffmpeg.decode)
        self.assertIn("h264_rkmpp", board.ffmpeg.encode)
        self.assertEqual(type(board.planner).__name__, "ClaudeApi")
        self.assertEqual(type(board.music).__name__, "Precomputed")

    def test_the_cases_match_the_sixth_versions(self):
        got = config.cases()
        self.assertEqual(got["pj"].index, "pinjie")
        self.assertEqual(CutOptions(free_order=True).instruction(got["ch"]), "剪一条川西旅行 vlog，场景之间衔接自然")

    def test_structured_outputs_close_every_object(self):
        got = planner.strict({"type": "object", "properties": {"a": {"type": "array", "items": {"type": "object"}}}})
        self.assertFalse(got["additionalProperties"])
        self.assertFalse(got["properties"]["a"]["items"]["additionalProperties"])


class WindowsTest(unittest.TestCase):
    def test_a_window_every_two_seconds_with_its_middle_frame(self):
        from montage_v8.domain import windows

        self.assertEqual(windows.windows(5000), [(0, 2000, 1000), (2000, 4000, 3000), (4000, 5000, 4500)])
        self.assertEqual(windows.windows(0), [])

    def test_videos_keep_their_numbers_and_new_ones_follow_in_time_order(self):
        from montage_v8.domain import windows

        first = windows.numbered([("u/b.mov", 2.0), ("u/a.mov", 1.0)], {})
        self.assertEqual(first, {"001": "u/a.mov", "002": "u/b.mov"})
        later = windows.numbered([("u/c.mov", 0.5), ("u/a.mov", 1.0), ("u/b.mov", 2.0)], {f: v for v, f in first.items()})
        self.assertEqual(later, {"001": "u/a.mov", "002": "u/b.mov", "003": "u/c.mov"})


class UploadNamesTest(unittest.TestCase):
    def test_names_stay_inside_the_uploads_folder_and_only_videos_are_taken(self):
        import tempfile

        from montage_v8.service.uploads import UploadError, Uploads

        with tempfile.TemporaryDirectory() as d:
            up = Uploads(Path(d))
            path, existed = up.target("../../etc", "../../x.mp4", 10)
            self.assertEqual(path, Path(d) / "etc" / "x.mp4")
            self.assertFalse(existed)
            for group, name in (("a", "x.sh"), ("..", "x.mp4"), ("a", "")):
                with self.assertRaises(UploadError):
                    up.target(group, name, 10)

    def test_the_same_file_again_is_not_kept_twice_and_a_different_one_gets_a_new_name(self):
        import tempfile

        from montage_v8.service.uploads import Uploads

        with tempfile.TemporaryDirectory() as d:
            up = Uploads(Path(d))
            (Path(d) / "湖").mkdir()
            (Path(d) / "湖" / "a.MOV").write_bytes(b"12345")
            self.assertEqual(up.target("湖", "a.MOV", 5), (Path(d) / "湖" / "a.MOV", True))
            self.assertEqual(up.target("湖", "a.MOV", 6), (Path(d) / "湖" / "a (2).MOV", False))
            self.assertEqual(up.listing(), [{"name": "湖", "files": [
                {"name": "a.MOV", "size": 5, "modified": up.listing()[0]["files"][0]["modified"]}]}])


class FakeMontage:
    """编排层的替身：记下被调用的步骤，按需要在某一步失败或被取消。"""

    def __init__(self, calls, fail=None, report=None, stop=None):
        self.calls, self.fail, self.report, self.stop = calls, fail, report, stop

    def _do(self, what):
        from montage_v8.pipeline import Cancelled

        if self.stop and self.stop():
            raise Cancelled
        if self.fail == what:
            raise RuntimeError(f"{what} 坏了")
        self.calls.append(what)

    def usable(self, case, opts):
        return [{"id": "x"}]

    def describe(self, case, opts):
        self._do("describe")
        if self.report:
            self.report("描述卡片 pj-001", None, 1, 2)

    def music(self, case, song):
        self._do("music")

    def index(self, case, folder):
        self._do(f"index {folder}")
        return {"001": "uploads/湖边/a.mov", "002": "uploads/湖边/b.mov"}

    def cards(self, case):
        self._do("cards")

    def quality(self, case):
        self._do("quality")

    def shots(self, case, opts, song):
        self._do(f"shots{opts.tag}")

    def cut(self, case, opts, song):
        self._do(f"cut{opts.tag}")


class JobsTest(unittest.TestCase):
    def setUp(self):
        import tempfile

        from montage_v8.jobs import CutRequest, Jobs

        self.tmp = tempfile.TemporaryDirectory()
        self.jobs = Jobs(Path(self.tmp.name) / "jobs.sqlite")
        self.cases = {"pj": CASE, "upload:湖边": CASE}
        self.resolve = self.cases.get
        self.req = CutRequest.parse({"case": "pj", "song": "S", "versions": 2, "ask": " 多用大景 "}, self.resolve, {"S"})

    def tearDown(self):
        self.tmp.cleanup()

    def worker(self, calls, **kw):
        from montage_v8.jobs import Worker

        return Worker(self.jobs, lambda **cb: FakeMontage(calls, report=cb.get("report"), stop=cb.get("stop"), **kw),
                      self.resolve, lambda montage, case, req, tags: {"tags": tags, "videos": req.videos})

    def test_a_request_is_checked_and_its_options_follow_the_page(self):
        from montage_v8.jobs import CutRequest, RequestError

        for bad in ({"case": "zz", "song": "S"}, {"case": "pj", "song": "nope"}, {"case": "pj", "song": "S", "versions": 9},
                    {"case": "pj", "song": "S", "order": "random"}):
            with self.assertRaises(RequestError):
                CutRequest.parse(bad, self.resolve, {"S"})
        opts = CutRequest.parse({"case": "pj", "song": "S", "videos": ["002", "001"], "order": "place", "ask": "多用大景"},
                                self.resolve, {"S"}).options("-x")
        self.assertTrue(opts.free_order)
        self.assertEqual(opts.videos, frozenset({"001", "002"}))
        self.assertEqual(opts.instruction(CASE), "剪一条旅行 vlog；多用大景")
        self.assertEqual(self.req.steps()[-1], "渲染方案2")

    def test_a_job_runs_every_step_once_and_keeps_its_result(self):
        job = self.jobs.add(self.req)
        calls = []
        self.worker(calls).run(self.jobs.next())
        got = self.jobs.get(job["id"])
        self.assertEqual(got["state"], "done")
        self.assertEqual(calls, ["describe", "music", f"shots-job{job['id']}-1", f"cut-job{job['id']}-1",
                                 f"shots-job{job['id']}-2", f"cut-job{job['id']}-2"])
        self.assertEqual(got["result"], {"tags": [f"-job{job['id']}-1", f"-job{job['id']}-2"], "videos": []})
        self.assertIsNone(self.jobs.next())

    def test_an_uploaded_folder_is_indexed_cut_and_checked_first_and_its_files_become_video_numbers(self):
        from montage_v8.jobs import CutRequest

        req = CutRequest.parse({"case": "upload:湖边", "song": "S", "versions": 1, "videos": ["b.mov"]}, self.resolve, {"S"})
        self.assertEqual(req.steps()[:4], ["检查素材", "给视频建索引", "按画面切成卡片", "检查模糊、抖动、遮挡"])
        job = self.jobs.add(req)
        calls = []
        self.worker(calls).run(self.jobs.next())
        got = self.jobs.get(job["id"])
        self.assertEqual(got["state"], "done", got["error"])
        self.assertEqual(calls[:4], ["index 湖边", "cards", "quality", "describe"])
        self.assertEqual(got["result"]["videos"], ["002"])
        self.assertEqual(got["step"], len(req.steps()))

    def test_a_failure_is_kept_with_its_step_and_the_next_job_still_runs(self):
        first, second = self.jobs.add(self.req), self.jobs.add(self.req)
        self.assertEqual(self.jobs.ahead(second["id"]), 1)
        self.worker([], fail="music").run(self.jobs.next())
        got = self.jobs.get(first["id"])
        self.assertEqual((got["state"], got["error"], got["detail"]), ("failed", "music 坏了", "第 3 步出错"))
        self.worker([]).run(self.jobs.next())
        self.assertEqual(self.jobs.get(second["id"])["state"], "done")

    def test_cancelling_stops_a_queued_job_at_once_and_a_running_one_before_its_next_step(self):
        from montage_v8.jobs import Jobs

        queued = self.jobs.add(self.req)
        self.assertEqual(self.jobs.cancel(queued["id"])["state"], "cancelled")
        running = self.jobs.add(self.req)
        self.jobs.next()
        self.jobs.cancel(running["id"])
        calls = []
        self.worker(calls).run(self.jobs.get(running["id"]))
        self.assertEqual(self.jobs.get(running["id"])["state"], "cancelled")
        self.assertEqual(calls, [])
        # a job running when the service stopped is queued again when it starts
        again = self.jobs.add(self.req)
        self.jobs.next()
        self.assertEqual(Jobs(self.jobs.path).get(again["id"])["state"], "queued")


try:
    import starlette  # noqa: F401  网页服务的依赖只在 ~/.venvs/montage-v8 之类的环境里装
    HAS_WEB = True
except ImportError:
    HAS_WEB = False


@unittest.skipUnless(HAS_WEB, "没有装 starlette、httpx（ai/montage_v8/requirements-web.txt）")
class UploadServiceTest(unittest.TestCase):
    def test_a_video_is_written_whole_and_listed_and_a_short_body_leaves_nothing(self):
        import tempfile

        from starlette.testclient import TestClient

        from montage_v8.service.app import create_app

        with tempfile.TemporaryDirectory() as d:
            client = TestClient(create_app(Path(d)))
            r = client.put("/api/uploads", params={"group": "湖边", "name": "a.mp4", "modified": "1700000000000"}, content=b"x" * 1000)
            self.assertEqual(r.status_code, 200, r.text)
            saved = Path(d) / "湖边" / "a.mp4"
            self.assertEqual(saved.read_bytes(), b"x" * 1000)
            self.assertEqual(int(saved.stat().st_mtime), 1700000000)
            self.assertEqual(client.get("/api/uploads").json()["groups"][0]["files"][0]["name"], "a.mp4")
            self.assertEqual(client.get("/uploads/湖边/a.mp4", headers={"Range": "bytes=0-9"}).status_code, 206)
            r = client.put("/api/uploads", params={"group": "湖边", "name": "b.mp4"}, content=b"x" * 10,
                           headers={"Content-Length": "20"})
            self.assertEqual(r.status_code, 400)
            self.assertEqual(sorted(p.name for p in (Path(d) / "湖边").iterdir()), ["a.mp4"])

    def test_search_requests_go_to_the_search_service_with_every_repeated_parameter(self):
        import tempfile
        from unittest import mock

        import httpx
        from starlette.testclient import TestClient

        from montage_v8.service.app import create_app

        seen = {}

        async def fake(self, method, url, **kw):
            seen.update(method=method, url=url, params=kw["params"])
            return httpx.Response(200, json={"scores": {}})

        with tempfile.TemporaryDirectory() as d, mock.patch.object(httpx.AsyncClient, "request", fake):
            client = TestClient(create_app(Path(d), proxy="http://search"))
            self.assertEqual(client.get("/likeness?of=a/1&to=b/1&to=c/1").status_code, 200)
        self.assertEqual(seen["params"], [("of", "a/1"), ("to", "b/1"), ("to", "c/1")])


if __name__ == "__main__":
    unittest.main()
