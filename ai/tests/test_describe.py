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

    def test_the_answer_checker_covers_the_entire_prompt_schema(self):
        supported = {
            "object": {"type", "properties", "required"},
            "array": {"type", "minItems", "maxItems", "items"},
            "string": {"type", "minLength", "maxLength", "enum"},
        }
        remaining = [describe.schema(3)]
        while remaining:
            node = remaining.pop()
            self.assertLessEqual(set(node), supported[node["type"]])
            remaining.extend(node.get("properties", {}).values())
            if "items" in node:
                remaining.append(node["items"])

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

    def test_a_card_that_fits_keeps_every_frame(self):
        offsets, groups = [0, 1.5, 3, 4.5], [1, 2, 2, 3]
        for most in (4, 20):
            kept, fitted_offsets, fitted_groups = describe.fitting(offsets, groups, most)
            self.assertEqual(kept, [0, 1, 2, 3])
            self.assertIs(fitted_offsets, offsets)
            self.assertIs(fitted_groups, groups)

    def test_a_card_past_its_budget_keeps_frames_spread_from_its_first_to_its_last(self):
        offsets = [2.0 * k for k in range(14)]
        kept, fitted_offsets, fitted_groups = describe.fitting(offsets, list(range(1, 15)), 11)
        self.assertEqual(kept, [0, 1, 3, 4, 5, 7, 8, 9, 10, 12, 13])
        self.assertEqual(fitted_offsets, [2.0 * k for k in kept])
        self.assertEqual(fitted_groups, list(range(1, 12)))
        self.assertTrue(describe.valid_layout(fitted_offsets, fitted_groups, 30))
        # Every count the protocol takes, down to every budget: as many frames
        # as fit, none twice, the first and the last among them.
        for count in range(describe.FEWEST_FRAMES, describe.MOST_FRAMES + 1):
            for most in range(describe.FEWEST_FRAMES, count):
                kept, _, _ = describe.fitting(list(range(count)), list(range(1, count + 1)), most)
                self.assertEqual((len(kept), kept[0], kept[-1]), (most, 0, count - 1), (count, most))
                self.assertEqual(kept, sorted(set(kept)), (count, most))

    def test_a_beat_that_keeps_no_frame_is_left_out(self):
        # Six frames in four beats; the three kept leave the second beat out.
        kept, offsets, groups = describe.fitting([0, 1, 2, 3, 4, 5], [1, 2, 3, 3, 4, 4], 3)
        self.assertEqual((kept, offsets, groups), ([0, 3, 5], [0, 3, 5], [1, 2, 3]))
        _, schema, spans = describe.request(offsets, groups, 6)
        answer = describe.numbered(describe.parse(self.answer(beats=[{"shot_size": "远景", "what": "峡湾"}] * 3), schema, spans), kept)
        # The frames count among the card's six, not among the three asked about.
        self.assertEqual([beat["frames"] for beat in answer["beats"]], [[1, 1], [4, 4], [6, 6]])

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
