"""Offline tests: meaningful ranking and conservative ties for incomplete labels."""
import unittest

from ranking_metrics import conservative_rank, summarize_ranks, summarize_aligned_scores


class RankingMetricsTests(unittest.TestCase):
    def test_relevant_first_and_below_other_candidate(self):
        self.assertEqual(conservative_rank([0.8, 0.3, 0.1], 0), 1)
        self.assertEqual(conservative_rank([0.8, 0.9, 0.1], 0), 2)

    def test_equal_scores_are_not_optimistically_counted_as_wins(self):
        self.assertEqual(conservative_rank([0.5, 0.5, 0.1], 0), 2)
        self.assertEqual(conservative_rank([0.5, 0.5, 0.5], 1), 3)

    def test_recall_mrr(self):
        metrics = summarize_ranks([1, 2, 5, 11])
        self.assertEqual(metrics, {
            'recall_at_1': 0.25,
            'recall_at_5': 0.75,
            'recall_at_10': 0.75,
            'mean_reciprocal_rank': 0.4477,
        })

    def test_aligned_pool_metrics_and_ties(self):
        metrics = summarize_aligned_scores([[1.0, 0.0, -1.0],
                                            [0.9, 0.9, -1.0],
                                            [0.8, 0.7, 0.5]])
        self.assertEqual(metrics['wins'], 1)
        self.assertEqual(metrics['pair_accuracy'], 0.3333)
        self.assertEqual(metrics['recall_at_1'], 0.3333)
        self.assertEqual(metrics['mean_reciprocal_rank'], 0.6111)
        self.assertEqual(metrics['recall_at_5'], 1.0)

    def test_bad_score_matrix_rejected(self):
        for matrix in ([], [[1]], [[1, 2], [3]],
                       [[float('nan'), 1], [0, 1]],
                       [[float('inf'), 0], [0, 1]]):
            with self.subTest(matrix=matrix), self.assertRaises(ValueError):
                summarize_aligned_scores(matrix)

    def test_refuses_empty_or_invalid_scores(self):
        for scores, index in (([], 0), ([1], -1), ([1], 1), ([float('nan')], 0),
                              ([float('inf')], 0)):
            with self.subTest(scores=scores, index=index), self.assertRaises(ValueError):
                conservative_rank(scores, index)
        with self.assertRaises(ValueError):
            summarize_ranks([])
        with self.assertRaises(ValueError):
            summarize_ranks([0])


if __name__ == '__main__':
    unittest.main()
