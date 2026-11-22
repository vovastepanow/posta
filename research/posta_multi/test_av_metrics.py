import unittest
import torch
from av_metrics import paired_media_metrics


class AVMetricsTests(unittest.TestCase):
    def test_perfect_and_collapsed_pairs(self):
        identity = torch.eye(4)
        perfect = paired_media_metrics(identity, identity)
        self.assertEqual(perfect['hit_at_1'], 1.)
        self.assertEqual(perfect['mean_reciprocal_rank'], 1.)
        collapsed = paired_media_metrics(torch.ones(4, 2), torch.ones(4, 2))
        self.assertEqual(collapsed['hit_at_1'], 0.)
        self.assertAlmostEqual(collapsed['mean_reciprocal_rank'], .25)

    def test_invalid_inputs_rejected(self):
        with self.assertRaises(ValueError):
            paired_media_metrics(torch.ones(1, 2), torch.ones(1, 2))
        with self.assertRaises(ValueError):
            paired_media_metrics(torch.full((2, 2), float('nan')), torch.ones(2, 2))


if __name__ == '__main__':
    unittest.main()
