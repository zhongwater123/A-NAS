"""Known geometric answers without models, OpenCV or ONNX Runtime (#427)."""

import unittest

from anas_ai import providers

try:
    import numpy as np
except ImportError:  # CI installs NumPy; standard-library-only runs may omit it.
    np = None


@unittest.skipIf(np is None, "geometry tests need NumPy")
class GeometryTest(unittest.TestCase):
    def test_recovers_rotation_scale_and_translation(self):
        source = [[1, 2], [5, 2], [2, 6], [-1, 3], [4, -2]]
        # A 90 degree rotation, scale 2 and translation (7, -3).
        target = [[3, -1], [3, 7], [-5, 1], [1, -5], [11, 5]]
        matrix = providers.similarity_transform(source, target)
        np.testing.assert_allclose(matrix, [[0, -2, 7], [2, 0, -3]], atol=1e-12)
        np.testing.assert_allclose(np.asarray(source) @ matrix[:, :2].T + matrix[:, 2], target, atol=1e-12)

    def test_similarity_never_reflects_a_face(self):
        # Reflection of an anisotropic, centred rectangle: the best proper
        # similarity has scale 3/5 and zero rotation/translation, not a mirror.
        source = [[-2, -1], [-2, 1], [2, -1], [2, 1]]
        target = [[-2, 1], [-2, -1], [2, 1], [2, -1]]
        np.testing.assert_allclose(providers.similarity_transform(source, target),
                                   [[0.6, 0, 0], [0, 0.6, 0]], atol=1e-12)

    def test_small_rotation_is_not_mistaken_for_a_reflection(self):
        source = np.array([[1, 2], [5, 2], [2, 6], [-1, 3], [4, -2]])
        angle = np.deg2rad(1)
        expected = 2 * np.array([[np.cos(angle), -np.sin(angle)], [np.sin(angle), np.cos(angle)]])
        target = source @ expected.T + [7, -3]
        matrix = providers.similarity_transform(source, target)
        np.testing.assert_allclose(matrix[:, :2], expected, rtol=0, atol=1e-12)
        np.testing.assert_allclose(matrix[:, 2], [7, -3], rtol=0, atol=1e-12)

    def test_float32_landmarks_are_fitted_in_double_precision(self):
        source = np.array([[1.25, 2.5], [5.5, 2.25], [2.75, 6.5], [-1.5, 3.25], [4.5, -2.5]], dtype=np.float32)
        expected = np.array([[0.75, -1, 7], [1, 0.75, -3]])
        # All coordinates are binary fractions, so float32 preserves the input
        # exactly; rounding during the fit must not move the recovered matrix.
        target = (source @ expected[:, :2].T + expected[:, 2]).astype(np.float32)
        np.testing.assert_allclose(providers.similarity_transform(source, target), expected, rtol=0, atol=1e-12)

    def test_animal_crops_have_the_expected_pixels_at_edges_and_with_margin(self):
        image = np.arange(80 * 120 * 3).reshape(80, 120, 3)
        for box, rows, columns in (
            # 20 x 20 box becomes a 22 x 22 square with a 10% margin.
            ([0.25, 0.25, 5 / 12, 0.5], (19, 41), (29, 51)),
            ([0, 0, 1 / 6, 0.25], (0, 22), (0, 22)),
            ([5 / 6, 0.75, 1, 1], (58, 80), (98, 120)),
            # Width dominates, but the square cannot exceed image height.
            ([0, 0, 1, 1], (0, 80), (20, 100)),
            # Height dominates; fractional coordinates truncate toward zero.
            ([0.5, 0.25, 0.55, 0.75], (18, 62), (41, 85)),
            ([0.5, 0.5, 0.5, 0.5], (39, 40), (59, 60)),
        ):
            with self.subTest(box=box):
                crop = providers.animal_crop(image, box)
                np.testing.assert_array_equal(crop, image[rows[0]:rows[1], columns[0]:columns[1]])

    def test_animal_crop_is_also_bounded_by_portrait_image_width(self):
        image = np.arange(120 * 80 * 3).reshape(120, 80, 3)
        np.testing.assert_array_equal(providers.animal_crop(image, [0, 0, 1, 1]), image[20:100, 0:80])
