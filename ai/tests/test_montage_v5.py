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
        self.assertEqual([len(p) for p in got[1]], [3, 2])

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


class OrderTest(unittest.TestCase):
    def test_every_shooting_order_ask_is_in_step_ones_prompt(self):
        text = cut.PICK + cut.music_note(SONG)
        for ask, _instead in cut.ORDER_ASKS:
            self.assertIn(ask, text)


if __name__ == "__main__":
    unittest.main()
