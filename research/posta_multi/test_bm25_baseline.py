"""Pure-CPU lexical reference tests, no user records or network."""
import unittest

from bm25_baseline import bm25_scores, score_pairs, words


class BM25ReferenceTests(unittest.TestCase):
    def test_unicode_cyrillic_nfkc_casefold(self):
        self.assertEqual(words('  ПРИВЕТ　мИР!  '), ['привет', 'мир'])
        self.assertEqual(words('APPLE banana; banana'), ['apple', 'banana', 'banana'])

    def test_relevance_ranks_above_unrelated_text(self):
        pairs = [
            ('red sports car', 'red sports car engine', 'vegetable soup'),
            ('healthy breakfast', 'healthy breakfast oats', 'car fuel'),
            ('мемы коты', 'смешные мемы коты', 'корпоративное управление'),
        ]
        metrics = score_pairs(pairs, 'stable16')
        self.assertEqual(metrics['samples'], 3)
        self.assertEqual(metrics['candidate_pool_size'], 6)
        self.assertEqual(metrics['pair_accuracy'], 1)
        self.assertEqual(metrics['recall_at_1'], 1)
        self.assertEqual(metrics['mean_reciprocal_rank'], 1)
        self.assertGreater(metrics['mean_positive_minus_negative'], 0)

    def test_empty_and_invalid_rejected(self):
        self.assertEqual(bm25_scores('', [['cat'], ['dog']]), [0, 0])
        self.assertEqual(bm25_scores('cat', [[], []]), [0, 0])
        with self.assertRaises(ValueError):
            bm25_scores('cat', [['cat']], b=1.5)
        with self.assertRaises(ValueError):
            score_pairs([], 'digest')


if __name__ == '__main__':
    unittest.main()
