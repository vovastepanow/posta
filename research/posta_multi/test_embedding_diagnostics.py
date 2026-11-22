"""CPU-only correctness tests for embedding-collapse diagnostics."""
import unittest

import torch

from embedding_diagnostics import geometry, paired_alignment, transformation_sensitivity


class GeometryTests(unittest.TestCase):
    def test_identical_embeddings_flag_collapse(self):
        metrics = geometry(torch.ones(6, 4))
        self.assertEqual(metrics["centered_effective_rank"], 0)
        self.assertAlmostEqual(metrics["off_diagonal_mean_cosine"], 1)
        self.assertEqual(metrics["off_diagonal_fraction_over_0_995"], 1)

    def test_orthogonal_vectors_are_distinct(self):
        metrics = geometry(torch.eye(4))
        self.assertAlmostEqual(metrics["off_diagonal_mean_cosine"], 0)
        self.assertAlmostEqual(metrics["centered_effective_rank"], 3)

    def test_perfect_alignment(self):
        metrics = paired_alignment(torch.eye(6), torch.eye(6))
        self.assertEqual(metrics["left_to_right"]["recall_at_1"], 1)
        self.assertEqual(metrics["right_to_left"]["recall_at_5"], 1)
        self.assertEqual(metrics["left_to_right"]["mrr"], 1)

    def test_tied_predictions_are_not_artificially_perfect(self):
        x = torch.ones(6, 4)
        metrics = paired_alignment(x, x)
        self.assertAlmostEqual(metrics["left_to_right"]["recall_at_1"], 1 / 6)
        self.assertAlmostEqual(metrics["right_to_left"]["recall_at_5"], 5 / 6)
        self.assertAlmostEqual(
            metrics["left_to_right"]["mean_positive_minus_hardest_negative"], 0)

    def test_reverse_order_sensitivity(self):
        before = torch.eye(3)
        after = before.roll(1, dims=1)
        metrics = transformation_sensitivity(before, after)
        self.assertAlmostEqual(metrics["mean_1_minus_cosine"], 1)

    def test_no_difference_is_detected(self):
        vectors = torch.eye(3)
        metrics = transformation_sensitivity(vectors, vectors)
        self.assertAlmostEqual(metrics["mean_1_minus_cosine"], 0)

    def test_invalid_inputs_rejected(self):
        with self.assertRaises(ValueError):
            geometry(torch.zeros(4, 5))
        with self.assertRaises(ValueError):
            paired_alignment(torch.ones(3, 4), torch.ones(2, 4))
        with self.assertRaises(ValueError):
            transformation_sensitivity(torch.ones(2, 4), torch.ones(3, 4))


if __name__ == "__main__":
    unittest.main()
