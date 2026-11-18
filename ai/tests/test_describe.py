"""The card description's prompt and schema (anas_ai.describe), standard library only."""

import json
import unittest

from anas_ai import describe


class DescribeTest(unittest.TestCase):
    def test_the_prompt_is_the_prototypes_with_the_numbers_sentence(self):
        content, schema, spans = describe.request([0, 1.6, 4.2], [1, 2, 3], 6.4)
        text = content[0][1]
        self.assertTrue(text.startswith("下面 3 张图按时间先后取自同一段视频（共 6 秒）"))
        self.assertIn("what 要写明是字幕、文字或黑屏。" + describe.NO_NUMBERS + "\n这类素材是「旅行 vlog」", text)
        self.assertEqual(text.count(describe.NO_NUMBERS), 1)
        self.assertIn("第 1 组：第 1 张；第 2 组：第 2 张；第 3 组：第 3 张", text)
        self.assertIn("process：写完 beats 再写", text)
        self.assertEqual(content[1:], [("text", "第 1 张（第 1 组，第 0 秒）"), ("image", 0),
                                       ("text", "第 2 张（第 2 组，第 2 秒）"), ("image", 1),
                                       ("text", "第 3 张（第 3 组，第 4 秒）"), ("image", 2)])
        self.assertEqual(spans, [[1], [2], [3]])
        self.assertEqual(list(schema["properties"]), ["camera", "beats", "process", "summary", "scene"])
        self.assertEqual(schema["required"], ["camera", "beats", "process", "summary", "scene"])
        self.assertEqual((schema["properties"]["beats"]["minItems"], schema["properties"]["beats"]["maxItems"]), (3, 3))
        self.assertEqual(schema["properties"]["summary"]["maxLength"], describe.SUMMARY_CHARS + 20)

    def test_grouped_frames_share_a_beat(self):
        content, schema, spans = describe.request([0, 1, 2, 3], [1, 1, 2, 2], 4)
        self.assertIn("第 1 组：第 1–2 张；第 2 组：第 3–4 张", content[0][1])
        self.assertEqual(spans, [[1, 2], [3, 4]])
        self.assertEqual(schema["properties"]["beats"]["maxItems"], 2)

    def answer(self, **change):
        answer = {"camera": "船上", "beats": [{"shot_size": "远景", "what": "峡湾"}, {"shot_size": "近景", "what": "栏杆"}],
                  "process": ["出航"], "summary": "游船驶过峡湾", "scene": "峡湾"}
        return json.dumps({**answer, **change}, ensure_ascii=False)

    def test_an_answer_is_checked_and_its_beats_get_their_frames(self):
        _, schema, spans = describe.request([0, 1, 2], [1, 1, 2], 3)
        answer = describe.parse("```json\n" + self.answer() + "\n```", schema, spans)
        self.assertEqual([beat["frames"] for beat in answer["beats"]], [[1, 2], [3, 3]])
        for change in ({"camera": "海上"}, {"beats": []}, {"beats": [{"shot_size": "远景", "what": "峡湾"}] * 3},
                       {"beats": [{"shot_size": "大景", "what": "峡湾"}] * 2}, {"beats": [{"what": "峡湾"}] * 2},
                       {"summary": ""}, {"summary": "长" * (describe.SUMMARY_CHARS + 21)}, {"scene": 3},
                       {"process": []}, {"process": "出航"}):
            with self.subTest(change), self.assertRaises(describe.InvalidAnswer):
                describe.parse(self.answer(**change), schema, spans)
        with self.assertRaises(describe.InvalidAnswer):
            describe.parse("{\"camera\": ", schema, spans)
        with self.assertRaises(describe.InvalidAnswer):
            describe.parse("[]", schema, spans)

    def test_a_layout_is_three_to_twenty_frames_in_order(self):
        self.assertTrue(describe.valid_layout([0, 1, 2], [1, 2, 3], 2))
        self.assertTrue(describe.valid_layout([0.5] * 20, [1] * 20, 3))
        for offsets, groups, duration in (([0, 1], [1, 2], 2), ([0] * 21, [1] * 21, 1), ([0, 1, 2], [1, 2], 2),
                                          ([0, 1, 2], [0, 1, 2], 2), ([0, 1, 2], [1, 2, 4], 2),
                                          ([0, 1, 2], [1, 2, 3], 1.5), ([0, 0, 0], [1, 2, 3], 0),
                                          ([0, 1, float("inf")], [1, 2, 3], float("inf")), ("012", [1, 2, 3], 2),
                                          ([0, 1, 2], [1.0, 2, 3], 2), ([0, 1, 2], [1, 2, 3], True)):
            self.assertFalse(describe.valid_layout(offsets, groups, duration), (offsets, groups, duration))


if __name__ == "__main__":
    unittest.main()
