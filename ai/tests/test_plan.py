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
        self.assertEqual(text.splitlines()[:2], ["指令：剪成预告片", "目标时长：成片约 60 秒"])
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
        self.assertIn("片段1（0:00–0:12，2 张卡片）", text)  # the planner sets seconds: no paces told
        self.assertNotIn("节奏", text)
        self.assertIn("01-1-0 可剪6秒 办公室里的男子", text)

    def test_a_part_of_long_card_lines_gives_its_summary(self):
        self.index.parts = [{"video": "01", "cards": ["01-0-0"], "summary": "雨夜街头"},
                            {"video": "01", "cards": ["01-1-0"], "summary": "办公室"}]
        self.index.captions["01-1-0"] = "办公室里的男子" * 60  # past moments.PART_CHARS
        lines = plan.message(self.index, "剪成预告片", 60, "预告片").splitlines()
        self.assertEqual(lines[-4:], ["片段1（0:00–0:06，1 张卡片）", "01-0-0 可剪6秒 雨中的街道",
                                      "片段2（0:06–0:12，1 张卡片）", "概述：办公室"])

    def test_the_lines_said_are_not_shown_to_the_planner(self):
        # 剪法23 (#416): the program keeps sentences whole at the cuts; the plan does not pick by them.
        self.index.said = {"01": [(1.0, 2.0, "Look"), (2.1, 3.0, " there.")]}
        text = plan.message(self.index, "剪成预告片", 60, "预告片")
        self.assertNotIn("台词", text)
        self.assertNotIn("Look", text)
        self.assertNotIn("said", plan.SYSTEM)

    def test_cards_with_a_title_over_them_are_left_out(self):
        self.index.texts = {"01-0-0": "片名：MERIDIAN"}
        text = plan.message(self.index, "剪成预告片", 60, "预告片")
        self.assertNotIn("01-0-0", text)
        self.assertIn("片段1（0:00–0:12，1 张卡片）", text)
        self.assertIn("01-1-0", text)

    def test_a_plan_of_paragraphs_is_sent_as_its_segments(self):
        answer = '{"story": "s", "paragraphs": [{"part": 2, "shots": 3, "weight": 7, "shows": "打斗", "reason": "高潮"}]}'
        planned = plan.parsed(answer)
        self.assertEqual(planned["segments"], planned["paragraphs"])
        body = plan.request(planned, 60, None)
        # One paragraph takes the whole target (剪法24).
        self.assertEqual(body["segments"], [{"part": 2, "shots": 3, "weight": 7, "shows": "打斗", "reason": "高潮",
                                             "probe": "打斗", "seconds": 60}])

    def test_the_seconds_are_shared_out_by_weight(self):
        # 剪法24 (#416): 1.5 s a shot first, the rest of the target by weight, adding up to it.
        answer = ('{"story": "s", "paragraphs": [{"card": "01-0-0", "shots": 2, "weight": 3, "shows": "街道"},'
                  '{"card": "01-1-0", "shots": 1, "weight": 1, "shows": "办公室"}]}')
        body = plan.request(plan.parsed(answer), 30, None)
        self.assertEqual([s["seconds"] for s in body["segments"]], [22.12, 7.88])
        self.assertAlmostEqual(sum(s["seconds"] for s in body["segments"]), 30)

    def test_a_paragraph_without_a_whole_weight_is_refused(self):
        for weight in ('"重"', "0", "11", "2.5", "true", None):
            field = "" if weight is None else f', "weight": {weight}'
            answer = '{"story": "s", "paragraphs": [{"card": "01-0-0", "shows": "街道"' + field + "}]}"
            with self.subTest(weight=weight), self.assertRaisesRegex(ValueError, "第 1 个段落的 weight"):
                plan.parsed(answer)

    def test_allocated_shares_the_target_and_keeps_each_shots_minimum(self):
        self.assertEqual(moments.allocated([1, 1], [1, 1], 10), [5.0, 5.0])
        self.assertEqual(moments.allocated([2, 1, 1], [1, 1, 1], 10), [4.25, 2.88, 2.87])
        # Shots' minimums past the target: just the minimums.
        self.assertEqual(moments.allocated([9, 1], [6, 2], 10), [9.0, 3.0])

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
        by_part = {"part": 2, "weight": 2, "shows": "办公室", "reason": "交代处境"}
        self.assertEqual(plan.request({"segments": [by_part | {"seconds": 9}]}, 60, None)["segments"],
                         [by_part | {"probe": "办公室", "seconds": 60}])  # a plan's own seconds are not passed on
        self.assertEqual({k: body[k] for k in ("seconds", "margin", "edges", "look", "pace")},
                         {"seconds": 60, "margin": False, "edges": True, "look": False, "pace": "picture"})

    def test_the_system_prompt_asks_for_every_field(self):
        for field in plan.FIELDS:
            self.assertIn(f'"{field}"', plan.SYSTEM)


if __name__ == "__main__":
    unittest.main()
