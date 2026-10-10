"""Consistency of the label vocabulary, its calibration and its ground truth."""

import json
import os
import re
import unittest

from eval import datasets

CATEGORIES = {"people", "animal", "vehicle", "food", "nature", "scene", "object", "document", "activity"}


class VocabularyTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.labels = datasets.load_vocabulary()
        cls.ids = {label["id"] for label in cls.labels}

    def test_labels_are_unique_and_well_formed(self):
        self.assertEqual(len(self.ids), len(self.labels))
        words = {}
        for label in self.labels:
            self.assertRegex(label["id"], r"^[a-z0-9_]+$")
            self.assertIn(label["category"], CATEGORIES, label["id"])
            for word in [label["name"]] + label["synonyms"]:
                self.assertTrue(word.strip(), label["id"])
                # A word names one label, or search could not tell them apart.
                self.assertNotIn(word, words, f"{label['id']} and {words.get(word)} share {word!r}")
                words[word] = label["id"]

    def test_ground_truth_maps_known_labels(self):
        for label, mapping in datasets.load_ground_truth().items():
            self.assertIn(label, self.ids)
            self.assertTrue(set(mapping) <= {"coco", "cocoAbsentUnless", "cocoStuff", "openimages"}, label)
            self.assertLessEqual(len(set(mapping) & {"coco", "cocoAbsentUnless", "cocoStuff"}), 1, label)
            self.assertTrue(mapping, label)

    def test_calibration_names_known_labels(self):
        path = os.path.join(os.path.dirname(datasets.VOCABULARY), "v1.calibration.json")
        if not os.path.exists(path):
            self.skipTest("not calibrated yet")
        with open(path, encoding="utf-8") as file:
            calibration = json.load(file)
        self.assertEqual(calibration["labels"], 1)
        self.assertRegex(calibration["model"], r"^embeddinggemma-2-740m@[0-9a-f]{12}\+")
        self.assertEqual(calibration["phrase"].count("{}"), 1)
        for label, threshold in calibration["thresholds"].items():
            self.assertIn(label, self.ids)
            self.assertTrue(-1 < threshold < 1, label)


if __name__ == "__main__":
    unittest.main()
