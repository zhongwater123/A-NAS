"""Tests with the real aesthetic scorer bundle (#480).

They run only when ANAS_AI_SCORER names the unpacked bundle, the directory
with its MANIFEST.json and model/, and PyTorch and transformers are installed
as ai/requirements.txt pins them; the manifest comes from deploy/models.
"""

import io
import math
import os
import threading
import time
import unittest

from anas_ai import providers

SCORER = os.environ.get("ANAS_AI_SCORER", "")
MANIFEST = os.path.join(os.path.dirname(__file__), "..", "..", "deploy", "models", "qwen35-4b-aesthetic-scorer.json")


def thumbnail():
    """A 512 px JPEG like the photo service's thumbnails: a sky over a field, and a sun."""
    from PIL import Image, ImageDraw

    image = Image.new("RGB", (512, 384))
    draw = ImageDraw.Draw(image)
    for y in range(384):
        draw.line([(0, y), (511, y)], fill=(70, 130, 200) if y < 240 else (60, 140 - (y - 240) // 4, 50))
    draw.ellipse((360, 50, 430, 120), fill=(250, 220, 120))
    out = io.BytesIO()
    image.save(out, format="JPEG", quality=85)
    return out.getvalue()


@unittest.skipUnless(SCORER, "set ANAS_AI_SCORER to the unpacked scorer bundle")
class RealScorerTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.scorer = providers.AestheticScorer(SCORER, MANIFEST)
        cls.photo = thumbnail()

    def test_describes_itself(self):
        self.assertTrue(self.scorer.model.startswith(
            "qwen35-4b-aesthetic-scorer-1.0.0@tea8d5b62a513+h18d96b2f91b8+torch-"), self.scorer.model)
        self.assertTrue(self.scorer.model.endswith("+px65536-1048576"), self.scorer.model)
        self.assertEqual(self.scorer.memory, 2 << 30)

    def test_scores_a_thumbnail_the_same_each_time(self):
        first = self.scorer.score(self.photo, providers.RunHandle())
        self.assertEqual(set(first), {"aesthetic", "technical"})
        for value in first.values():
            # Not clipped to 1 to 5, but a photo lands near that range.
            self.assertTrue(math.isfinite(value) and 0 < value < 6, first)
        self.assertEqual(first, self.scorer.score(self.photo, providers.RunHandle()))

    def test_rejects_bytes_that_are_not_an_image(self):
        with self.assertRaises(providers.InvalidInput):
            self.scorer.score(b"this is not an image", providers.RunHandle())

    def test_stops_inside_the_tower_when_told(self):
        self.scorer.score(self.photo, providers.RunHandle())
        run = providers.RunHandle()
        threading.Timer(0.3, lambda: setattr(run, "terminate", True)).start()
        started = time.monotonic()
        with self.assertRaises(providers.Cancelled):
            self.scorer.score(self.photo, run)
        self.assertLess(time.monotonic() - started, 5)
        # The scorer is left as it was and scores the next photo.
        self.assertIn("aesthetic", self.scorer.score(self.photo, providers.RunHandle()))

    def test_release_drops_the_model_and_the_next_photo_loads_it(self):
        before = self.scorer.score(self.photo, providers.RunHandle())
        self.scorer.release()
        self.assertIsNone(self.scorer._scorer)
        self.assertEqual(before, self.scorer.score(self.photo, providers.RunHandle()))


if __name__ == "__main__":
    unittest.main()
