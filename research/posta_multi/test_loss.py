"""Synthetic tests for retrieval objective; no dataset or GPU required."""
import unittest
import torch
from loss import retrieval_loss


class RetrievalLossTests(unittest.TestCase):
    def test_relevant_candidates_reduce_loss_and_gradient_flows(self):
        query = torch.tensor([[1., 0.], [0., 1.]], requires_grad=True)
        candidates = torch.tensor([[1., 0.], [0., 1.]], requires_grad=True)
        positive = torch.eye(2, dtype=torch.bool)
        good = retrieval_loss(query, candidates, positive)
        bad = retrieval_loss(query, candidates.flip(0), positive)
        self.assertLess(good.item(), bad.item())
        bad.backward()
        self.assertTrue(torch.isfinite(query.grad).all())
        self.assertGreater(query.grad.abs().sum().item(), 0)

    def test_multiple_positives_and_ignored_pairs(self):
        query = torch.tensor([[1., 0.]])
        candidates = torch.tensor([[1., 0.], [0., 1.], [-1., 0.]])
        positives = torch.tensor([[True, True, False]])
        ignored = torch.tensor([[False, False, True]])
        self.assertAlmostEqual(retrieval_loss(query, candidates, positives, ignored).item(), 0., places=6)
        self.assertGreater(retrieval_loss(query, candidates, positives, temperature=1).item(), 0.)

    def test_invalid_masks_are_rejected(self):
        x = torch.ones(1, 2)
        with self.assertRaises(ValueError):
            retrieval_loss(x, x, torch.tensor([[False]]))
        with self.assertRaises(ValueError):
            retrieval_loss(x, x, torch.tensor([[True]]), torch.tensor([[True]]))


if __name__ == '__main__':
    unittest.main()
