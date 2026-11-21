"""CPU-only tests for the new from-scratch compact retriever."""
import unittest
from dataclasses import replace

import torch

from compact import (CompactConfig, CompactRetriever, paired_contrastive_loss,
                     trainable_parameters)


def tiny():
    return CompactConfig(vocab_size=300, width=32, layers=2, heads=4,
                         ff_width=64, max_length=16, output_dim=16, dropout=0)


class CompactTests(unittest.TestCase):
    def test_parameter_count_and_distinct_blocks(self):
        c = tiny()
        model = CompactRetriever(c)
        self.assertEqual(sum(p.numel() for p in model.parameters()), trainable_parameters(c))
        self.assertFalse(torch.equal(model.encoder.layers[0].linear1.weight,
                                     model.encoder.layers[1].linear1.weight))
        self.assertLess(trainable_parameters(CompactConfig()), 12_000_000)

    def test_identical_padding_invariant_and_gradient(self):
        torch.manual_seed(12)
        model = CompactRetriever(tiny())
        model.eval()
        ids = torch.tensor([[1, 7, 8, 2, 0, 0], [1, 8, 9, 2, 0, 0]])
        mask = ids.ne(0)
        changed = ids.clone()
        changed[:, -2:] = 25
        a = model.encode(ids, mask)
        b = model.encode(changed, mask)
        self.assertTrue(torch.allclose(a, b, atol=1e-6))
        self.assertTrue(torch.allclose(a.norm(dim=-1), torch.ones(2), atol=1e-5))
        negative = model.encode(torch.tensor([[1, 16, 17, 2, 0, 0],
                                              [1, 17, 18, 2, 0, 0]]), mask)
        loss = paired_contrastive_loss(a, a, negative)
        self.assertTrue(torch.isfinite(loss))
        loss.backward()
        self.assertGreater(model.project.weight.grad.abs().sum().item(), 0)

    def test_training_step_lower_loss_on_simple_pair(self):
        torch.manual_seed(123)
        m = CompactRetriever(tiny())
        m.eval()
        query = torch.tensor([[1, 5, 7, 2]])
        pos = torch.tensor([[1, 5, 7, 2]])
        neg = torch.tensor([[1, 16, 20, 2]])
        mask = query.ne(0)
        opt = torch.optim.AdamW(m.parameters(), lr=0.002)
        before = paired_contrastive_loss(m.encode(query, mask), m.encode(pos, mask),
                                         m.encode(neg, mask)).item()
        for _ in range(7):
            opt.zero_grad()
            loss = paired_contrastive_loss(
                m.encode(query, mask), m.encode(pos, mask), m.encode(neg, mask))
            loss.backward()
            opt.step()
        after = paired_contrastive_loss(m.encode(query, mask), m.encode(pos, mask),
                                        m.encode(neg, mask)).item()
        self.assertLess(after, before)

    def test_invalid_tokens_and_config_refused(self):
        with self.assertRaises(ValueError):
            CompactRetriever(replace(tiny(), heads=5))
        m = CompactRetriever(tiny())
        with self.assertRaises(ValueError):
            m.encode(torch.tensor([[1, 350, 2]]), torch.ones(1, 3, dtype=torch.bool))
        with self.assertRaises(ValueError):
            m.encode(torch.tensor([[1, 2, 0]]), torch.tensor([[True, True, False]]))
        with self.assertRaises(ValueError):
            paired_contrastive_loss(torch.ones(1, 2), torch.ones(2, 2), torch.ones(1, 2))


if __name__ == '__main__':
    unittest.main()
