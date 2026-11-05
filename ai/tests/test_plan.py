"""The planning request (montage.plan): one message of what the model needs, and its JSON answer."""

import unittest

try:
    import numpy as np

    from montage import moments, plan
except ImportError:
    np = plan = None


@unittest.skipIf(plan is None, "needs NumPy")
class PlanTest(unittest.TestCase):
    def setUp(self):
        basis = np.eye(4, dtype=np.float32)
        windows = [{"start": 2 * k, "end": 2 * k + 2, "frame": 2000 * k + 1000, "flat": False, "tags": []}
                   for k in range(6)]
        self.index = moments.Index({"model": "test", "videos": {"01": {
            "file": "01.mp4", "duration": 12.0, "shots": [[0, 6], [6, 12]], "windows": windows}}},
            {f"01/{w['frame']}": basis[0] if k < 3 else basis[1] for k, w in enumerate(windows)})
        self.index.captions = {"01-0-0": "雨中的街道", "01-1-0": "办公室里的男子"}
        self.index.scene_of, self.index.scene_names = {"01-0-0": 1, "01-1-0": 2}, {1: "室外，街道", 2: "室内，办公室"}

    def test_the_message_holds_the_instruction_seconds_style_and_cards_by_part(self):
        text = plan.message(self.index, "剪成预告片", 60, "预告片")
        self.assertEqual(text.splitlines()[:2], ["指令：剪成预告片", "目标时长：约 60 秒"])
        self.assertIn("体裁：预告片", text)
        self.assertNotIn("剪法", text)  # a trailer is given no rules of its own
        # Without parts written, the video is one part.
        self.assertEqual(text.splitlines()[-3:], ["片段1（0:00–0:12，2 张卡片）", "01-0-0 可剪6秒 雨中的街道",
                                                  "01-1-0 可剪6秒 办公室里的男子"])
        self.assertNotIn("场景", text)
        self.assertNotIn("秒（镜头", text)  # no card seconds: the program sets them
        looks = {"motion": "固定", "sharp": 500.0, "shake": 0.0, "bright": 100.0, "clipped": 0.0}
        self.index.pictures = {"01-0-0": looks | {"change": 0.002}, "01-1-0": looks | {"change": 0.08}}
        text = plan.message(self.index, "剪成预告片", 60, "预告片")
        self.assertIn("片段1（0:00–0:12，2 张卡片：慢 1、快 1）", text)
        self.assertIn("01-0-0 可剪6秒 节奏慢", text)
        self.assertIn("01-1-0 可剪6秒 节奏快", text)  # a fast card's seconds are not cut to its pace

    def test_a_part_of_long_card_lines_gives_its_summary(self):
        self.index.parts = [{"video": "01", "cards": ["01-0-0"], "summary": "雨夜街头"},
                            {"video": "01", "cards": ["01-1-0"], "summary": "办公室"}]
        self.index.captions["01-1-0"] = "办公室里的男子" * 60  # past moments.PART_CHARS
        lines = plan.message(self.index, "剪成预告片", 60, "预告片").splitlines()
        self.assertEqual(lines[-4:], ["片段1（0:00–0:06，1 张卡片）", "01-0-0 可剪6秒 雨中的街道",
                                      "片段2（0:06–0:12，1 张卡片）", "概述：办公室"])

    def test_cards_with_a_title_over_them_are_left_out(self):
        self.index.texts = {"01-0-0": "片名：MERIDIAN"}
        text = plan.message(self.index, "剪成预告片", 60, "预告片")
        self.assertNotIn("01-0-0", text)
        self.assertIn("片段1（0:00–0:12，1 张卡片）", text)
        self.assertIn("01-1-0", text)

    def test_the_answer_is_one_json_object_fenced_or_not(self):
        answer = '故事线如下。\n```json\n{"story": "s", "segments": [{"card": "01-0-0", "shows": "街道", "pace": "慢"}]}\n```'
        self.assertEqual(plan.parsed(answer)["segments"][0]["card"], "01-0-0")
        for bad in ("没有计划", '{"story": "s", "segments": []}'):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                plan.parsed(bad)

    def test_the_request_locates_each_segment_by_what_it_shows(self):
        segment = {"card": "01-0-0", "shows": "街道", "reason": "交代地点"}
        body = plan.request({"segments": [segment | {"pace": "快", "extra": "不传"}]}, 60, "预告片")
        self.assertEqual(body["segments"], [segment | {"probe": "街道"}])  # the program paces it
        by_part = {"part": 2, "seconds": 2.5, "shows": "办公室", "reason": "交代处境"}
        self.assertEqual(plan.request({"segments": [by_part]}, 60, None)["segments"], [by_part | {"probe": "办公室"}])
        self.assertEqual({k: body[k] for k in ("seconds", "margin", "edges", "look", "pace")},
                         {"seconds": 60, "margin": True, "edges": True, "look": False, "pace": "picture"})

    def test_the_system_prompt_asks_for_every_field(self):
        for field in plan.FIELDS:
            self.assertIn(f'"{field}"', plan.SYSTEM)


if __name__ == "__main__":
    unittest.main()
