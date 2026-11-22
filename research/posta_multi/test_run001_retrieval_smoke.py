"""Synthetic token shape/provenance checks for retrieval GPU smoke."""
import unittest
import tempfile
from pathlib import Path
from unittest.mock import patch

from run001_retrieval_smoke import synthetic_triples, run


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

    def test_invalid_probe_length_rejected_before_model_allocation(self):
        with tempfile.TemporaryDirectory() as tmp, \
             patch('run001_retrieval_smoke.torch.cuda.is_available', return_value=True), \
             patch('run001_retrieval_smoke.torch.cuda.set_per_process_memory_fraction'), \
             patch('run001_retrieval_smoke.torch.manual_seed'), \
             patch('run001_retrieval_smoke.PostaMulti') as model:
            with self.assertRaisesRegex(ValueError, 'dimensions'):
                run(Path(tmp) / 'probe.json', length=513)
            model.assert_not_called()


if __name__ == '__main__':
    unittest.main()
