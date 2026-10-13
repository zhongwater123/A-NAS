"""Tests with the real D-FINE Objects365 detector.

They run only when ANAS_AI_DETECTOR names the exported .onnx file and ONNX
Runtime, NumPy and Pillow are installed (docs/development/LOCAL_ENVIRONMENT.md);
the manifest comes from deploy/models.
"""

import io
import os
import unittest

from anas_ai import providers

DETECTOR = os.environ.get("ANAS_AI_DETECTOR", "")
MANIFEST = os.path.join(os.path.dirname(__file__), "..", "..", "deploy", "models", "dfine-m-obj365.json")


def photo():
    """A synthetic scene: a few solid shapes on a textured background."""
    from PIL import Image, ImageDraw

    image = Image.new("RGB", (1600, 1200), (180, 200, 220))
    draw = ImageDraw.Draw(image)
    for i in range(0, 1600, 40):
        draw.line([(i, 0), (i, 1200)], fill=(170, 190, 210), width=3)
    draw.rectangle((300, 500, 700, 1100), fill=(120, 60, 30))
    draw.ellipse((900, 300, 1300, 700), fill=(240, 220, 40))
    out = io.BytesIO()
    image.save(out, format="JPEG", quality=92)
    return out.getvalue()


@unittest.skipUnless(DETECTOR, "set ANAS_AI_DETECTOR to the exported .onnx file")
class RealDetectorTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.detector = providers.OnnxDetector(DETECTOR, MANIFEST)

    def test_describes_itself(self):
        self.assertIn("+ort-", self.detector.model)
        self.assertTrue(self.detector.model.endswith(f"+{providers.DETECTOR_INPUT}+s0.05k100"))
        self.assertGreaterEqual(self.detector.threads, 1)

    def test_boxes_are_well_formed_and_repeatable(self):
        data = photo()
        first = self.detector.detect(data, self.detector.run_options())
        again = self.detector.detect(data, self.detector.run_options())
        self.assertEqual(first, again)
        self.assertLessEqual(len(first), providers.MAX_BOXES)
        scores = [box[1] for box in first]
        self.assertEqual(scores, sorted(scores, reverse=True))
        for label, score, x0, y0, x1, y1 in first:
            self.assertTrue(0 <= label < 366)
            self.assertTrue(providers.MIN_BOX_SCORE <= score <= 1)
            self.assertTrue(0 <= x0 <= x1 <= 1 and 0 <= y0 <= y1 <= 1)

    def test_a_terminated_run_is_cancelled(self):
        run = self.detector.run_options()
        run.terminate = True
        with self.assertRaises(providers.Cancelled):
            self.detector.detect(photo(), run)

    def test_release_drops_the_session_and_detection_loads_it_again(self):
        self.detector.detect(photo(), self.detector.run_options())
        self.detector.release()
        self.assertTrue(self.detector.detect(photo(), self.detector.run_options()) is not None)

    def test_rejects_what_is_not_an_image(self):
        with self.assertRaises(providers.InvalidInput):
            self.detector.detect(b"not an image", self.detector.run_options())


if __name__ == "__main__":
    unittest.main()
