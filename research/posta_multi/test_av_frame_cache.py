"""Gradient-equivalence tests for CPU-backed frame replay (no CUDA required)."""
import copy
import unittest

import torch

from av_frame_cache import cached_frame_embeddings, backward_recomputed_frames
from model import PostaMulti
from synthetic_train import tiny_config


class FrameCacheTests(unittest.TestCase):
    def setUp(self):
        torch.manual_seed(874)
        torch.set_num_threads(2)
        self.model = PostaMulti(tiny_config(), dropout=0).eval()

    def test_cached_replay_matches_direct_image_gradient(self):
        pixels = torch.rand(1, 3, 3, self.model.config.image_size,
                            self.model.config.image_size)
        reference = copy.deepcopy(self.model).eval()
        vectors = torch.stack(
            [reference.encode_image(pixels[:, i]) for i in range(pixels.shape[1])],
            dim=1)
        weights = torch.randn_like(vectors)
        (vectors * weights).sum().backward()
        expected = reference.image_patches.weight.grad.detach().clone()

        leaf = cached_frame_embeddings(self.model, pixels)
        self.assertTrue(leaf.is_leaf)
        self.assertTrue(leaf.requires_grad)
        self.assertTrue(torch.allclose(vectors, leaf, atol=1e-6))
        (leaf * weights).sum().backward()
        backward_recomputed_frames(self.model, pixels, leaf)
        actual = self.model.image_patches.weight.grad
        self.assertTrue(torch.allclose(actual, expected, rtol=1e-4, atol=1e-5))
        self.assertIsNone(self.model.text_tokens.weight.grad)

    def test_replay_rejects_unbackpropagated_and_corrupt_gradients(self):
        pixels = torch.rand(1, 2, 3, self.model.config.image_size,
                            self.model.config.image_size)
        leaf = cached_frame_embeddings(self.model, pixels)
        with self.assertRaisesRegex(ValueError, "backpropagate"):
            backward_recomputed_frames(self.model, pixels, leaf)
        leaf.grad = torch.full_like(leaf, float("nan"))
        with self.assertRaisesRegex(ValueError, "nonfinite"):
            backward_recomputed_frames(self.model, pixels, leaf)

    def test_training_mode_or_pixel_gradients_rejected(self):
        pixels = torch.rand(1, 2, 3, self.model.config.image_size,
                            self.model.config.image_size)
        pixels.requires_grad_()
        with self.assertRaisesRegex(ValueError, "pixel gradients"):
            cached_frame_embeddings(self.model, pixels)
        pixels = pixels.detach()
        self.model.train()
        with self.assertRaisesRegex(ValueError, "eval mode"):
            cached_frame_embeddings(self.model, pixels)


if __name__ == "__main__":
    unittest.main()
