import unittest
from hybrid_retrieval import conservative_ranks, fuse_scores, compare_splits


class HybridRetrievalTests(unittest.TestCase):
    def test_ties_and_score_scales(self):
        self.assertEqual(conservative_ranks([2, 2, 1]), [2, 2, 3])
        a = fuse_scores([8, 5, 0], [0.2, 0.8, 0.1], semantic_weight=.5)
        b = fuse_scores([800, 500, 0], [20, 80, 10], semantic_weight=.5)
        self.assertEqual(a, b)
        with self.assertRaises(ValueError):
            fuse_scores([1], [1, 2], semantic_weight=.5)
        with self.assertRaises(ValueError):
            fuse_scores([float('nan')], [1], semantic_weight=.5)

    def test_calibration_does_not_look_at_other_queries(self):
        first = {'lexical': [1, 2], 'semantic': [2, 1], 'positive': 0, 'negative': 1}
        last = {'lexical': [2, 1], 'semantic': [1, 2], 'positive': 0, 'negative': 1}
        result = compare_splits([first, last], 1, weights=(0.0, 1.0))
        self.assertEqual(result['selected_semantic_weight'], 1.0)
        self.assertEqual(result['evaluation']['selected_hybrid']['recall_at_1'], 0.0)
        self.assertEqual(result['evaluation']['bm25']['recall_at_1'], 1.0)
        with self.assertRaises(ValueError):
            compare_splits([first, last], 2)


if __name__ == '__main__':
    unittest.main()
