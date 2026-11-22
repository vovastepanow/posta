"""CPU-only correctness and collapse-detection tests for AV diagnostics."""
import unittest

import torch
import torch.nn.functional as F

from av_geometry_diagnostics import pairwise_geometry


class GeometryDiagnosticsTests(unittest.TestCase):
    def test_detects_media_collapse_relative_to_diverse_text(self):
        text = torch.eye(4)
        media = torch.tensor([[1., 0.], [1., 0.0001],
                              [1., -0.0001], [1., 0.0002]])
        out = pairwise_geometry(text, media)
        self.assertEqual(out["samples"], 4)
        self.assertEqual(out["sample_pairs"], 6)
        self.assertAlmostEqual(out["text_interexample_cosine"], 0.0)
        self.assertGreater(out["media_interexample_cosine"], 0.999)
        self.assertGreater(out["mean_absolute_geometry_error"], 0.99)
        self.assertIsNone(out["geometry_pearson_correlation"])

    def test_aligned_pairwise_geometry_near_zero_error(self):
        torch.manual_seed(11)
        values = F.normalize(torch.randn(8, 12), dim=-1)
        out = pairwise_geometry(values, values)
        self.assertLess(out["mean_absolute_geometry_error"], 1e-6)
        self.assertAlmostEqual(out["geometry_pearson_correlation"], 1.0, places=5)

    def test_rejects_nonfinite_and_too_small(self):
        with self.assertRaises(ValueError):
            pairwise_geometry(torch.ones(2, 3), torch.ones(2, 3))
        values = torch.eye(4)
        altered = values.clone()
        altered[0, 0] = float("nan")
        with self.assertRaises(ValueError):
            pairwise_geometry(values, altered)
        altered = values.clone()
        altered[0] = 0.
        with self.assertRaisesRegex(ValueError, "zero-norm"):
            pairwise_geometry(values, altered)


if __name__ == "__main__":
    unittest.main()
