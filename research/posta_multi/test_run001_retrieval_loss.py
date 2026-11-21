"""Anti-collapse regression tests independent of HDD and CUDA."""
import unittest

import torch
import torch.nn.functional as F

from run001_retrieval_loss import (anti_collapse_loss, checkpoint_is_healthy,
                                   positive_only_alignment_loss)


class AntiCollapseTests(unittest.TestCase):
    def test_strong_match_has_lower_loss_than_wrong_match(self):
        query = torch.eye(4, requires_grad=True)
        positives = torch.eye(4, requires_grad=True)
        wrong = torch.roll(torch.eye(4), shifts=1, dims=0)
        good, diag = anti_collapse_loss(query, positives, wrong)
        bad, _ = anti_collapse_loss(query, wrong, positives)
        self.assertLess(float(good), float(bad))
        self.assertAlmostEqual(diag['pair_accuracy'], 1.0)
        bad.backward()
        self.assertTrue(bool(torch.isfinite(query.grad).all()))
        self.assertGreater(float(query.grad.abs().sum()), 0)

    def test_near_collapse_has_gradient_to_separate_queries(self):
        torch.manual_seed(12)
        q = (torch.ones(8, 24) * 0.2 +
             torch.randn(8, 24) * 1e-3).requires_grad_()
        p = (torch.ones(8, 24) * 0.2 +
             torch.randn(8, 24) * 1e-3).requires_grad_()
        n = (torch.ones(8, 24) * 0.2 +
             torch.randn(8, 24) * 1e-3).requires_grad_()
        loss, details = anti_collapse_loss(q, p, n)
        self.assertGreater(details['query_interexample_cosine'], .99)
        self.assertGreater(details['variance_penalty'], 0)
        self.assertGreater(details['repulsion_penalty'], 0)
        loss.backward()
        self.assertGreater(float(q.grad.norm()), 1e-5)
        self.assertGreater(float(p.grad.norm()), 1e-5)
        self.assertTrue(bool(torch.isfinite(q.grad).all()))

    def test_gradient_actually_reduces_collapse_loss(self):
        torch.manual_seed(3)
        query = torch.nn.Parameter(torch.ones(4, 12) + 0.05 * torch.randn(4, 12))
        positive = torch.nn.Parameter(query.detach().clone() + 0.01 * torch.randn(4, 12))
        negative = torch.roll(positive.detach(), 1, 0)
        opt = torch.optim.Adam([query, positive], lr=0.01)
        before, _ = anti_collapse_loss(query, positive, negative)
        for _ in range(8):
            opt.zero_grad()
            loss, _ = anti_collapse_loss(query, positive, negative)
            loss.backward()
            opt.step()
        after, diag = anti_collapse_loss(query, positive, negative)
        self.assertLess(float(after), float(before))
        self.assertTrue(torch.isfinite(after))

    def test_validation_gates_legacy_collapsed_metrics(self):
        healthy = {
            'pair_accuracy': .8, 'mean_positive_minus_negative': .02,
            'query_interexample_cosine': .92,
            'document_interexample_cosine': .93,
            'mean_reciprocal_rank': .34,
        }
        self.assertTrue(checkpoint_is_healthy(healthy))
        unhealthy = dict(healthy, query_interexample_cosine=.998,
                         document_interexample_cosine=.999,
                         mean_positive_minus_negative=-0.0002,
                         pair_accuracy=.44)
        self.assertFalse(checkpoint_is_healthy(unhealthy))
        self.assertFalse(checkpoint_is_healthy(dict(healthy, mean_reciprocal_rank=float('nan'))))
        self.assertFalse(checkpoint_is_healthy({}))

    def test_positive_only_alignment_uses_no_fabricated_negatives(self):
        q = torch.eye(4, requires_grad=True)
        p = torch.eye(4, requires_grad=True)
        aligned, diag = positive_only_alignment_loss(q, p)
        shuffled, _ = positive_only_alignment_loss(q, torch.roll(p, 1, 0))
        self.assertLess(float(aligned), float(shuffled))
        self.assertAlmostEqual(diag['alignment_loss'], 0.0, places=6)
        shuffled.backward()
        self.assertGreater(float(q.grad.abs().sum()), 0)
        self.assertTrue(bool(torch.isfinite(q.grad).all()))

    def test_positive_only_near_collapse_has_nonzero_gradient(self):
        torch.manual_seed(108)
        q = (torch.ones(4, 16)*0.2 +
             torch.randn(4, 16)*0.02).requires_grad_()
        p = (torch.ones(4, 16)*0.2 +
             torch.randn(4, 16)*0.02).requires_grad_()
        loss, diag = positive_only_alignment_loss(q, p)
        self.assertGreater(diag['query_interexample_cosine'], .95)
        self.assertGreater(diag['variance_penalty'], 0)
        loss.backward()
        self.assertGreater(float(q.grad.norm()), 1e-6)
        self.assertTrue(bool(torch.isfinite(q.grad).all()))
        with self.assertRaises(ValueError):
            positive_only_alignment_loss(q[:1], p[:1])

    def test_invalid_and_nonfinite_data_are_rejected(self):
        q = torch.ones(2, 6)
        with self.assertRaises(ValueError):
            anti_collapse_loss(q, q[:1], q)
        with self.assertRaises(ValueError):
            anti_collapse_loss(q, q, torch.full_like(q, float('nan')))
        with self.assertRaises(ValueError):
            anti_collapse_loss(q, q, q, temperature=0)
        with self.assertRaises(ValueError):
            anti_collapse_loss(q, q, q, minimum_std=float('nan'))


if __name__ == '__main__':
    unittest.main()
