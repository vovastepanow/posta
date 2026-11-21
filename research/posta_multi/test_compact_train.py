"""Focused CPU unit tests for real compact retrieval batch training."""
import tempfile
import unittest
from pathlib import Path

import torch

from compact import CompactConfig, CompactRetriever
from compact_train import (batch_loss, batch_tensors, better, encode_pairs,
                           learning_rate, require_training_approval, validate)


class FakeEncoding:
    def __init__(self, text):
        self.ids = [1, 4 + (ord(text[0]) % 40 if text else 0), 2]


class FakeTokenizer:
    def enable_truncation(self, max_length):
        self.maximum = max_length
    def encode(self, text):
        return FakeEncoding(text)


class CompactTrainTests(unittest.TestCase):
    def setUp(self):
        torch.manual_seed(42)
        self.model = CompactRetriever(CompactConfig(
            vocab_size=300, width=32, layers=1, heads=4, ff_width=64,
            max_length=16, output_dim=16, dropout=0))
        self.triples = encode_pairs(FakeTokenizer(), [
            ('Query One', 'Positive One', 'Negative One'),
            ('Query Two', 'Positive Two', 'Negative Two'),
            ('Query Three', 'Positive Three', 'Negative Three'),
        ], 16)

    def test_batch_loss_is_finite_and_trainable(self):
        m = self.model
        original = m.project.weight.detach().clone()
        optimizer = torch.optim.AdamW(m.parameters(), lr=0.001)
        optimizer.zero_grad()
        loss = batch_loss(m, self.triples[:2], 'cpu')
        self.assertTrue(torch.isfinite(loss))
        loss.backward()
        torch.nn.utils.clip_grad_norm_(m.parameters(), max_norm=1)
        optimizer.step()
        self.assertFalse(torch.equal(original, m.project.weight))

    def test_holdout_returns_pool_ranking_and_no_raw_text(self):
        metrics = validate(self.model, self.triples, 'cpu', batch_size=2)
        self.assertEqual(metrics['samples'], 3)
        self.assertGreater(metrics['documents'], 1)
        self.assertTrue(0 <= metrics['recall_at_1'] <= 1)
        self.assertTrue(0 <= metrics['mean_reciprocal_rank'] <= 1)
        self.assertNotIn('Positive', str(metrics))

    def test_approval_is_explicit_and_fail_closed(self):
        for value in ({}, {'status': 'screened'},
                      {'status': 'approved_for_training'},
                      {'status': 'approved_for_training',
                       'privacy_review': 'passed', 'source_rights_review': 'pending'}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                require_training_approval(value)
        require_training_approval({
            'status': 'approved_for_training',
            'privacy_review': 'passed', 'source_rights_review': 'passed',
        })

    def test_schedule_and_best_selection(self):
        self.assertAlmostEqual(learning_rate(1, 100, 0.001, 10), 0.0001)
        self.assertAlmostEqual(learning_rate(10, 100, 0.001, 10), 0.001)
        self.assertAlmostEqual(learning_rate(100, 100, 0.001, 10), 0.0001)
        current = {'mean_reciprocal_rank': 0.3, 'pair_accuracy': 0.75}
        new = {'mean_reciprocal_rank': 0.4, 'pair_accuracy': 0.5}
        self.assertFalse(better(new, current))
        self.assertTrue(better({'mean_reciprocal_rank': 0.4,
                                'pair_accuracy': 0.75}, current))

    def test_padding_and_invalid_batches(self):
        tokens, mask = batch_tensors([[1, 5, 2], [1, 9, 10, 2]], 'cpu')
        self.assertEqual(tokens.shape, (2, 4))
        self.assertEqual(mask.sum().item(), 7)
        with self.assertRaises(ValueError):
            batch_tensors([], 'cpu')
        with self.assertRaises(ValueError):
            batch_loss(self.model, [], 'cpu')


if __name__ == '__main__':
    unittest.main()
