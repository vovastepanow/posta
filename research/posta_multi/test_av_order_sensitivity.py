"""CPU shape, chronology and gradient tests for full-clip reversal probes."""
import unittest
from dataclasses import replace

import torch

from av_order_sensitivity import encode_reversed_video
from av_temporal import aggregate_video_windows
from model import PostaMulti
from synthetic_train import tiny_config


class OrderSensitivityTests(unittest.TestCase):
    def setUp(self):
        torch.manual_seed(23)
        torch.set_num_threads(2)
        self.config = replace(tiny_config(), temporal_length=4)
        self.model = PostaMulti(self.config, dropout=0).eval()

    def test_reversal_across_unequal_window_boundaries(self):
        d = self.config.embed_dim
        frames = torch.randn(1, 6, d, requires_grad=True)
        windows = [frames[:, :3], frames[:, 3:5], frames[:, 5:]]
        times = [torch.tensor([[0., .5, 1.]]),
                 torch.tensor([[1.5, 2.]]),
                 torch.tensor([[2.5]])]
        actual = encode_reversed_video(self.model, windows, times,
                                       window_aggregation="hierarchical-residual-v1")
        flattened = frames.flip(1)
        cursor, parts = 0, []
        for original, stamp in zip(windows, times):
            chunk = flattened[:, cursor:cursor + original.shape[1]]
            parts.append(self.model.encode_video_embeddings(
                chunk, timestamps=stamp, time_encoding="relative-centered-v1").float())
            cursor += original.shape[1]
        expected = aggregate_video_windows(
            self.model, torch.stack(parts, dim=1),
            torch.tensor([[0., 1.5, 2.5]]),
            mode="hierarchical-residual-v1", time_encoding="relative-centered-v1")
        self.assertTrue(torch.allclose(actual, expected, atol=1e-6))
        actual[0, 0].backward()
        self.assertIsNotNone(frames.grad)
        self.assertTrue(torch.isfinite(frames.grad).all())
        self.assertGreater(float(frames.grad.abs().sum()), 0)

    def test_single_window_reverses_frame_sequence(self):
        frames = torch.randn(1, 3, self.config.embed_dim)
        times = torch.tensor([[0., .5, 1.]])
        actual = encode_reversed_video(
            self.model, [frames], [times], window_aggregation="mean")
        expected = self.model.encode_video_embeddings(
            frames.flip(1), timestamps=times, time_encoding="relative-centered-v1")
        self.assertTrue(torch.allclose(actual, expected, atol=1e-6))

    def test_nonchronological_and_bad_dimensions_rejected(self):
        d = self.config.embed_dim
        frames = torch.randn(1, 2, d)
        with self.assertRaisesRegex(ValueError, "matching"):
            encode_reversed_video(self.model, [], [])
        with self.assertRaisesRegex(ValueError, "advance"):
            encode_reversed_video(self.model, [frames, frames],
                                  [torch.tensor([[0., 1.]]), torch.tensor([[1., 2.]])])
        with self.assertRaisesRegex(ValueError, "invalid frame"):
            encode_reversed_video(self.model, [frames],
                                  [torch.tensor([[1., 0.]])])
        with self.assertRaisesRegex(ValueError, "invalid frame"):
            encode_reversed_video(self.model, [frames[:, :, :-1]],
                                  [torch.tensor([[0., 1.]])])


if __name__ == "__main__":
    unittest.main()
