"""Tests for relative media geometry, with no synthetic negative labels."""
import unittest

import torch
import torch.nn.functional as F

from av_geometry_objective import geometry_distillation_loss, alignment_with_geometry
from run001_retrieval_loss import positive_only_alignment_loss


class GeometryTrainingTests(unittest.TestCase):
    def test_identical_media_is_penalized_for_diverse_teacher(self):
        text = torch.eye(4)
        media = torch.tensor([[1., 0., 0., 0.],
                              [1., .001, 0., 0.],
                              [1., 0., .001, 0.],
                              [1., 0., 0., .001]], requires_grad=True)
        loss, stats = geometry_distillation_loss(text, media)
        self.assertGreater(float(loss), 1.0)
        self.assertGreater(stats["reference_mean_chord"], 1.4)
        self.assertLess(stats["media_mean_chord"], .01)
        loss.backward()
        self.assertIsNotNone(media.grad)
        self.assertTrue(torch.isfinite(media.grad).all())
        self.assertGreater(float(media.grad.abs().sum()), 0)

    def test_matched_teacher_geometry_is_zero_and_gradients_do_not_touch_teacher(self):
        torch.manual_seed(4)
        text = F.normalize(torch.randn(6, 16), dim=-1).requires_grad_(True)
        media = text.detach().clone().requires_grad_(True)
        loss, _ = geometry_distillation_loss(text, media)
        self.assertLess(float(loss), 1e-10)
        loss.backward()
        self.assertIsNone(text.grad)
        self.assertTrue(torch.isfinite(media.grad).all())

    def test_composite_objective_preserves_positive_only_loss_and_extra_terms(self):
        torch.manual_seed(5)
        target = F.normalize(torch.randn(4, 16), dim=-1)
        media = F.normalize(torch.randn(4, 16), dim=-1).requires_grad_(True)
        baseline, _ = positive_only_alignment_loss(target, media)
        added, stats = alignment_with_geometry(
            target, media, positive_only_alignment_loss, geometry_weight=.5)
        self.assertGreaterEqual(float(added), float(baseline))
        self.assertGreater(stats["teacher_geometry_loss"], 0)
        added.backward()
        self.assertTrue(torch.isfinite(media.grad).all())

    def test_rejects_two_pairs_and_invalid_weight(self):
        with self.assertRaisesRegex(ValueError, ">=3"):
            geometry_distillation_loss(torch.ones(2, 4), torch.ones(2, 4))
        with self.assertRaises(ValueError):
            alignment_with_geometry(torch.eye(4), torch.eye(4),
                                    positive_only_alignment_loss, geometry_weight=-1)


if __name__ == "__main__":
    unittest.main()
