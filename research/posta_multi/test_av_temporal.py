import unittest
from dataclasses import replace

import torch

from av_temporal import aggregate_video_windows
from model import PostaMulti
from synthetic_train import tiny_config


class TemporalAggregationTests(unittest.TestCase):
    def test_hierarchical_supports_long_sequences_and_order_with_gradients(self):
        torch.manual_seed(12)
        torch.set_num_threads(2)
        config = replace(tiny_config(), temporal_length=4)
        model = PostaMulti(config, dropout=0).eval()
        vectors = torch.randn(1, 9, config.embed_dim, requires_grad=True)
        timestamps = torch.arange(9, dtype=torch.float32).unsqueeze(0)
        output = aggregate_video_windows(model, vectors, timestamps, mode='hierarchical-v1',
                                         time_encoding='relative-centered-v1')
        reversed_output = aggregate_video_windows(model, vectors.flip(1), timestamps, mode='hierarchical-v1',
                                                  time_encoding='relative-centered-v1')
        self.assertEqual(output.shape, (1, config.embed_dim))
        self.assertFalse(torch.allclose(output, reversed_output, atol=1e-5))
        output[:, 0].sum().backward()
        self.assertTrue(torch.isfinite(vectors.grad).all())
        self.assertTrue((vectors.grad.abs().sum(dim=-1) > 0).all())
        residual = aggregate_video_windows(model, vectors, timestamps, mode='hierarchical-residual-v1',
                                           time_encoding='relative-centered-v1')
        self.assertTrue(torch.isfinite(residual).all())
        self.assertTrue(torch.allclose(residual.norm(dim=-1), torch.ones(1), atol=1e-6))

    def test_mean_preserved_and_nonchronological_rejected(self):
        model = PostaMulti(tiny_config(), dropout=0).eval()
        vectors = torch.randn(1, 3, model.config.embed_dim)
        times = torch.tensor([[0., 1., 2.]])
        expected = torch.nn.functional.normalize(vectors.mean(dim=1), dim=-1)
        self.assertTrue(torch.allclose(aggregate_video_windows(model, vectors, times), expected))
        with self.assertRaisesRegex(ValueError, 'chronological'):
            aggregate_video_windows(model, vectors, times.flip(1), mode='hierarchical-v1')


if __name__ == '__main__':
    unittest.main()
