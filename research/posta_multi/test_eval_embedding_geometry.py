import tempfile
import unittest
from pathlib import Path

import numpy as np

from eval_embedding_geometry import evaluate_npz


class GeometryNPZTests(unittest.TestCase):
    def test_report_includes_geometry_alignment_and_order(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "embedding.npz"
            x = np.eye(6, dtype=np.float32)
            np.savez(path, queries=x, posts=x, reversed_posts=np.roll(x, 1, axis=1))
            report = evaluate_npz(path)
            self.assertEqual(report["samples"], 6)
            self.assertEqual(report["dimensions"], 6)
            self.assertEqual(report["paired_retrieval"]["left_to_right"]["recall_at_1"], 1)
            self.assertGreater(report["temporal_order_sensitivity"]["mean_1_minus_cosine"], .99)
            self.assertEqual(len(report["input_sha256"]), 64)

    def test_missing_and_mismatched_vectors_are_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "bad.npz"
            np.savez(path, queries=np.eye(3))
            with self.assertRaisesRegex(ValueError, "queries and posts"):
                evaluate_npz(path)
            np.savez(path, queries=np.eye(3), posts=np.eye(4))
            with self.assertRaisesRegex(ValueError, "matching"):
                evaluate_npz(path)
            np.savez(path, queries=np.eye(3), posts=np.eye(3),
                     reversed_posts=np.eye(4))
            with self.assertRaisesRegex(ValueError, "reversed_posts"):
                evaluate_npz(path)

    def test_nonfinite_vectors_are_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "nan.npz"
            x = np.eye(3)
            x[0, 0] = np.nan
            np.savez(path, queries=x, posts=x)
            with self.assertRaisesRegex(ValueError, "nonfinite|non-finite"):
                evaluate_npz(path)


if __name__ == "__main__":
    unittest.main()
