"""Synthetic token shape/provenance checks for retrieval GPU smoke."""
import unittest

from run001_retrieval_smoke import synthetic_triples


class RetrievalSmokeTests(unittest.TestCase):
    def test_stable_synthetic_triples_and_no_user_text(self):
        pairs = synthetic_triples(300, length=12, samples=4, seed=12)
        self.assertEqual(pairs, synthetic_triples(300, 12, 4, 12))
        self.assertEqual(len(pairs), 4)
        for q, p, n in pairs:
            self.assertEqual(len(q), 12)
            self.assertEqual(q[0], 1)
            self.assertEqual(q[-1], 2)
            self.assertNotEqual(q, p)
            self.assertNotEqual(q, n)
            self.assertTrue(all(4 <= x < 300 for x in q[1:-1]))
        with self.assertRaises(ValueError):
            synthetic_triples(300, length=1)


if __name__ == '__main__':
    unittest.main()
