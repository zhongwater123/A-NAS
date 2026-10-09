"""Tests with the real EmbeddingGemma 2 model.

They run only when ANAS_AI_MODEL names the model file and MediaPipe is
installed (docs/development/LOCAL_ENVIRONMENT.md); the manifest comes from
deploy/models.
"""

import io
import os
import unittest

from anas_ai import providers

MODEL = os.environ.get("ANAS_AI_MODEL", "")
MANIFEST = os.path.join(os.path.dirname(__file__), "..", "..", "deploy", "models", "embeddinggemma-2-740m.json")


def drawing(kind, color):
    from PIL import Image, ImageDraw

    image = Image.new("RGB", (512, 384), "white")
    draw = ImageDraw.Draw(image)
    box = (156, 92, 356, 292)
    (draw.ellipse if kind == "circle" else draw.rectangle)(box, fill=color)
    out = io.BytesIO()
    image.save(out, format="JPEG", quality=85)
    return out.getvalue()


@unittest.skipUnless(MODEL, "set ANAS_AI_MODEL to the model file")
class RealModelTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.provider = providers.MediaPipeProvider(MODEL, MANIFEST)
        cls.embedder = cls.provider._embedder  # pylint: disable=protected-access

    def text(self, label):
        result = self.embedder.embed_text(f"task: search result | query: {label}")
        return [float(value) for value in result.embeddings[0].embedding]

    def test_describes_itself(self):
        self.assertEqual(self.provider.dimensions, 768)
        self.assertTrue(self.provider.model.startswith("embeddinggemma-2-740m@e7a8a2204b91+mediapipe-"))
        self.assertTrue(self.provider.model.endswith("+tok70+l2"))

    def test_matches_simple_drawings_to_chinese_labels(self):
        labels = ["红色的圆形", "蓝色的正方形", "一只猫", "一辆汽车"]
        vectors = {label: self.text(label) for label in labels}
        for label, image in (("红色的圆形", drawing("circle", "red")), ("蓝色的正方形", drawing("square", "blue"))):
            vector = self.provider.embed_image(image)
            self.assertAlmostEqual(sum(value * value for value in vector), 1.0, places=3)
            scores = {name: sum(a * b for a, b in zip(vector, other)) for name, other in vectors.items()}
            self.assertEqual(max(scores, key=scores.get), label, scores)

    def test_rejects_bytes_that_are_not_an_image(self):
        with self.assertRaises(providers.InvalidInput):
            self.provider.embed_image(b"this is not an image")


if __name__ == "__main__":
    unittest.main()
