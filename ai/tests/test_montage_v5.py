"""第六版 (eval/montage_v5): the rules that need no model, video or song: quality, slots, beats, the order's asks."""
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "eval" / "montage_v5"))
import cut

SONG = {"song": "/songs/a.mp3", "seconds": 24.0, "parts": [
    {"start": 0.0, "end": 8.0, "level": "中等", "slots": [[0.0, 4.0], [4.0, 8.0]]},
    {"start": 8.0, "end": 24.0, "level": "高潮", "slots": [[8.0, 10.0], [10.0, 12.0], [12.0, 14.0], [14.0, 16.0], [16.0, 18.0]]}]}


class QualityTest(unittest.TestCase):
    def test_a_dark_card_with_few_sharp_points_is_out_of_focus(self):
        self.assertTrue(cut.out_of_focus([22.0, 27.0, 27.0]))          # 新西兰 ne-070's soft stars
        self.assertFalse(cut.out_of_focus([635.0, 723.0, 754.0]))      # the starry sky over the church
        self.assertFalse(cut.out_of_focus([None, None, 22.0]))         # mostly not dark: not judged
        self.assertFalse(cut.out_of_focus([]))


class SlotsTest(unittest.TestCase):
    def test_a_parts_slots_are_shared_among_its_pictures_the_earlier_taking_the_odd_ones(self):
        parts = [{"scenes": [{}]}, {"scenes": [{}, {}]}]
        got = cut.slotted(SONG, parts)
        self.assertEqual([len(p) for p in got[0]], [2])
        self.assertEqual(got[1], [[[8.0, 14.0]], [[14.0, 18.0]]])     # a chorus picture is one shot

    def test_a_chorus_picture_is_one_take_across_its_bar_lines(self):
        self.assertEqual(cut.one_take([[77.43, 79.36], [79.36, 81.3]], "高潮"), [[77.43, 81.3]])
        self.assertEqual(cut.one_take([[60.01, 63.88], [63.88, 67.75]], "中等"), [[60.01, 63.88], [63.88, 67.75]])
        self.assertEqual(cut.one_take([[77.43, 79.36]], "高潮"), [[77.43, 79.36]])

    def test_a_pictures_slots_go_in_shooting_order_each_with_its_fills(self):
        slots = [[0.0, 4.0], [4.0, 8.0], [8.0, 12.0]]
        pieces = [{"card": "dv-015", "start": 8.0, "slot": [0.0, 4.0]},       # step two's first beat, shot last
                  {"card": "dv-015", "start": 0.2, "slot": [4.0, 8.0]},
                  {"card": "dv-015", "start": 4.9, "slot": [8.0, 12.0]},
                  {"card": "dv-016", "start": 0.0, "slot": [8.0, 12.0]}]      # the third slot's fill
        rank = {"dv-015": 15, "dv-016": 16}.get
        got = cut.in_shooting_order(pieces, slots, 30, rank)
        self.assertEqual([(x["card"], x["start"], x["slot"]) for x in got],
                         [("dv-015", 0.2, [0.0, 4.0]), ("dv-015", 4.9, [4.0, 8.0]), ("dv-016", 0.0, [4.0, 8.0]),
                          ("dv-015", 8.0, [8.0, 12.0])])

    def test_what_a_slots_first_beat_leaves_comes_from_another_card_first(self):
        # 潜水: dv-003/1 held 2.2 s of 3.9; dv-002 and dv-004 are the pictures either side, dv-008 one later on.
        left = ["dv-003/2", "dv-008/1", "dv-004/1", "dv-009/1", "dv-003/3", "dv-002/1"]
        got = cut.fill_order(left, "003", lambda b: b[3:6], lambda b: b[:6],
                             elsewhere={"dv-002", "dv-004", "dv-008"}, beside={"dv-002", "dv-004"})
        self.assertEqual(got, ["dv-009/1", "dv-004/1", "dv-002/1", "dv-003/2", "dv-003/3", "dv-008/1"])

    def test_slots_a_frame_apart_from_rounding_still_go_in_shooting_order(self):
        slots = [[27.11, 30.98], [30.98, 34.85], [34.85, 38.72]]       # 116, 117 and 116 frames at 30 fps
        pieces = [{"card": "dv-008", "start": s, "slot": slot} for s, slot in zip((1.0, 9.5, 5.4), slots, strict=True)]
        got = cut.in_shooting_order(pieces, slots, 30, lambda _c: 0)
        self.assertEqual([x["start"] for x in got], [1.0, 5.4, 9.5])

    def test_slots_of_different_lengths_move_with_their_lengths(self):
        # 潜水 dj-alldays-1's chorus: one recording cut at the six-second cap into 5.8, 5.8 and 3.9 s.
        slots = [[77.43, 83.24], [83.24, 89.05], [89.05, 92.92]]
        pieces = [{"card": "dj-009", "start": s, "slot": slot} for s, slot in zip((36.4, 11.1, 0.0), slots, strict=True)]
        got = cut.in_shooting_order(pieces, slots, 30, lambda _c: 0)
        self.assertEqual([(x["start"], x["slot"]) for x in got],
                         [(0.0, [77.43, 81.3]), (11.1, [81.3, 87.11]), (36.4, [87.11, 92.92])])

    def test_step_one_reads_each_part_with_its_level_and_shots(self):
        note = cut.music_note(SONG)
        self.assertIn("按音乐分成 2 段", note)
        self.assertIn("第 2 段 8–24 秒，高潮，能放 5 个镜头（每个约 2.0 秒）", note)


class BeatsTest(unittest.TestCase):
    def test_a_beat_runs_from_its_key_frame_at_least_shortest_at_most_cap(self):
        card = {"start": 10.0, "end": 30.0, "times": [11.0, 12.0, 29.5],
                "answer": {"beats": [{"end": 12.0}, {"end": 29.5}, {"end": 30.0}]}}
        got = cut.units(card, cut.CAP)
        self.assertEqual(got[0], (1, 10.0, 2.0))
        self.assertEqual(got[1], (2, 12.0, cut.CAP))
        self.assertEqual(got[2], (3, 28.0, 2.0))      # the last beat starts earlier to run SHORTEST


class RunTest(unittest.TestCase):
    def test_one_video_gives_at_most_three_shots_in_a_row_two_of_them_small(self):
        kart = [("054", "远景"), ("054", "中景"), ("054", "近景")]       # pj-058 to pj-060, one ride
        self.assertTrue(cut.breaks_run(kart, "054", "远景", set()))       # a fourth from the ride
        self.assertTrue(cut.breaks_run(kart, "054", "中景", set()))
        self.assertFalse(cut.breaks_run(kart, "056", "中景", set()))      # another video starts its own run
        self.assertTrue(cut.breaks_run(kart[1:], "054", "特写", set()))   # a third small one
        self.assertFalse(cut.breaks_run(kart[1:], "054", "全景", set()))
        self.assertFalse(cut.breaks_run([("054", "中景"), ("054", "远景")], "054", "近景", set()))  # small ones apart
        self.assertFalse(cut.breaks_run([], "054", "特写", set()))


class PlaceTest(unittest.TestCase):
    def test_the_scene_carries_the_camera_when_it_was_on_something(self):
        self.assertEqual(cut.place({"scene": "米尔福德峡湾", "camera": "船上"}), "米尔福德峡湾（船上）")
        self.assertEqual(cut.place({"scene": "湖边", "camera": "地面"}), "湖边")
        self.assertEqual(cut.place({"scene": "湖边"}), "湖边")             # described before 2026-11-16


class OrderTest(unittest.TestCase):
    def test_every_shooting_order_ask_is_in_step_ones_prompt(self):
        text = cut.PICK + cut.music_note(SONG)
        for ask, _instead in cut.ORDER_ASKS:
            self.assertIn(ask, text)


if __name__ == "__main__":
    unittest.main()
