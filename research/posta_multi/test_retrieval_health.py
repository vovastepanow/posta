"""CPU-only tests for retrieval embedding separation diagnostics."""
import unittest

import torch

from retrieval_health import off_diagonal_cosine, retrieval_health


class RetrievalHealthTests(unittest.TestCase):
    def test_orthogonal_embeddings_with_large_positive_margins(self):
        q = torch.eye(3)
        result = retrieval_health(q, q.clone(), [1, 1, 1], [0, 0, 0])
        self.assertEqual(result['mean_positive_minus_negative'], 1.0)
        self.assertEqual(result['margin_std'], 0)
        self.assertEqual(result['fraction_positive_margin'], 1)
        self.assertEqual(result['query_interexample_cosine'], 0)
        self.assertEqual(result['document_interexample_cosine'], 0)

    def test_identical_embeddings_and_tied_scores_indicate_collapse(self):
        q = torch.tensor([[1., 0.], [1., 0.], [1., 0.]])
        result = retrieval_health(q, q.clone(), [0.5]*3, [0.5]*3)
        self.assertEqual(result['mean_positive_minus_negative'], 0)
        self.assertEqual(result['fraction_positive_margin'], 0)
        self.assertEqual(result['query_interexample_cosine'], 1)
        self.assertEqual(result['document_interexample_cosine'], 1)

    def test_mixed_margins(self):
        q = torch.eye(3)
        result = retrieval_health(q, q, [0.8, 0.2, -0.1], [0.1, 0.3, 0.0])
        self.assertAlmostEqual(result['mean_positive_minus_negative'], 1/6, places=6)
        self.assertEqual(result['median_margin'], -0.1)
        self.assertAlmostEqual(result['fraction_positive_margin'], 1/3, places=6)

    def test_invalid_input_refused(self):
        with self.assertRaises(ValueError):
            off_diagonal_cosine(torch.ones(1, 3))
        with self.assertRaises(ValueError):
            retrieval_health(torch.ones(3, 4), torch.ones(3, 5), [1], [0])
        with self.assertRaises(ValueError):
            retrieval_health(torch.ones(3, 4), torch.ones(3, 4), [], [])
        with self.assertRaises(ValueError):
            retrieval_health(torch.ones(3, 4), torch.ones(3, 4), [float('nan')], [0])
        with self.assertRaises(ValueError):
            retrieval_health(torch.ones(3, 4), torch.ones(3, 4), [1], [float('inf')])
        with self.assertRaises(ValueError):
            off_diagonal_cosine(torch.tensor([[float('nan'), 1.], [0., 1.]]))


if __name__ == '__main__':
    unittest.main()
