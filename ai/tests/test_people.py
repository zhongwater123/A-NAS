"""The clustering rule the people evaluation measures (eval.people)."""

import unittest

try:
    import numpy as np
except ImportError:  # the Worker's own tests run with the standard library alone
    np = None


def unit(*values):
    vector = np.zeros(8, np.float32)
    vector[: len(values)] = values
    return vector / np.linalg.norm(vector)


@unittest.skipIf(np is None, "the evaluation needs NumPy")
class ClusterTest(unittest.TestCase):
    def occurrences(self, rows):
        from eval import people

        return [people.Occurrence(f"o{i}", photo, identity, unit(*vector)) for i, (photo, identity, vector) in enumerate(rows)]

    def test_founds_a_person_only_at_min_core(self):
        from eval import people

        two = self.occurrences([("a", "x", (1, 0.1)), ("b", "x", (1, 0.12))])
        self.assertEqual(people.cluster(two, 0.9, min_core=3), [-1, -1])
        three = self.occurrences([("a", "x", (1, 0.1)), ("b", "x", (1, 0.12)), ("c", "x", (1, 0.11))])
        self.assertEqual(people.cluster(three, 0.9, min_core=3), [0, 0, 0])

    def test_two_faces_of_one_photo_never_share_a_person(self):
        from eval import people

        rows = [("a", "x", (1, 0.1)), ("b", "x", (1, 0.12)), ("c", "x", (1, 0.11)),
                ("c", "y", (1, 0.105))]  # a look-alike beside x in photo c
        person = people.cluster(self.occurrences(rows), 0.9, min_core=3)
        self.assertNotEqual(person[2], person[3])
        self.assertEqual(person[0], person[1])

    def test_a_founder_takes_its_unassigned_neighbours_along(self):
        from eval import people

        rows = [("a", "x", (1, 0.1)), ("b", "x", (1, 0.12)), ("c", "x", (1, 0.11)), ("d", "x", (1, 0.13))]
        self.assertEqual(people.cluster(self.occurrences(rows), 0.9, min_core=3, retry=False), [0, 0, 0, 0])

    def test_unassigned_occurrences_get_a_second_chance(self):
        import math

        from eval import people

        def at(degrees):
            return (math.cos(math.radians(degrees)), math.sin(math.radians(degrees)))

        # 18° arrives first and is no neighbour of the founder at 6°, which
        # founds the person with 12° and 0°; only the second pass reaches it.
        rows = [("a", "x", at(18)), ("b", "x", at(12)), ("c", "x", at(0)), ("d", "x", at(6))]
        threshold = math.cos(math.radians(7))
        self.assertEqual(people.cluster(self.occurrences(rows), threshold, min_core=3, retry=False), [-1, 0, 0, 0])
        self.assertEqual(people.cluster(self.occurrences(rows), threshold, min_core=3), [0, 0, 0, 0])

    def test_score_counts_pairs(self):
        from eval import people

        occurrences = self.occurrences([("a", "x", (1,)), ("b", "x", (1,)), ("c", "y", (1,)), ("d", "y", (0, 1))])
        found = people.score(occurrences, [0, 0, 0, -1])
        # Person 0 holds x, x, y: one right pair of three; x has one pair, y one.
        self.assertAlmostEqual(found["precision"], 1 / 3)
        self.assertAlmostEqual(found["recall"], 1 / 2)
        self.assertEqual(found["mixedPeople"], 1)
        self.assertAlmostEqual(found["coverage"], 3 / 4)


if __name__ == "__main__":
    unittest.main()
