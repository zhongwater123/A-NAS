"""The index's shot cuts: a cut whose two sides show the same place joins its shots (#416)."""

import importlib.util
import unittest

HAVE = all(importlib.util.find_spec(name) for name in ("numpy", "PIL"))


@unittest.skipUnless(HAVE, "eval.video_moments needs NumPy and Pillow")
class SamePlaceTest(unittest.TestCase):
    def test_a_cut_whose_sides_are_alike_is_joined_and_the_rest_kept(self):
        from eval import video_moments as vm

        # Before 10 s one picture; from 10 s to 20 s the same place in other light; then another place.
        street, lit, river = [1.0, 0.0, 0.0], [0.95, 0.31, 0.0], [0.0, 0.0, 1.0]

        def vector_at(t):
            return street if t < 10 else lit if t < 20 else river

        kept, joined = vm.same_place([10.0, 20.0], vector_at)
        self.assertEqual(kept, [20.0])
        self.assertEqual([t for t, _ in joined], [10.0])
        self.assertGreaterEqual(joined[0][1], vm.SAME_PLACE)

    def test_the_two_sides_are_taken_either_side_of_the_cut(self):
        from eval import video_moments as vm

        asked = []
        vm.same_place([5.0], lambda t: asked.append(t) or [1.0, 0.0])
        self.assertEqual(asked, [5.0 - vm.SAME_PLACE_SECONDS, 5.0 + vm.SAME_PLACE_SECONDS])


if __name__ == "__main__":
    unittest.main()
