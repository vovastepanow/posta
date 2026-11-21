"""Tests of bounded-memory exact gradient cache and anti-collapse gates."""
import unittest
from dataclasses import replace

import torch

from model import PostaMulti
from synthetic_train import tiny_config
from run001_retrieval import (cached_retrieval_backward,
                              cached_positive_only_backward, evaluate_retrieval,
                              better_candidate, configure_trainable_stage,
                              encode_triples)
from run001_retrieval_loss import (anti_collapse_loss,
                                   positive_only_alignment_loss)


def fixture():
    return [
        ((1, 4, 5, 2), (1, 4, 5, 2), (1, 20, 22, 2)),
        ((1, 6, 7, 2), (1, 6, 7, 2), (1, 21, 23, 2)),
        ((1, 8, 9, 2), (1, 8, 9, 2), (1, 22, 24, 2)),
    ]


class RetrievalCacheTests(unittest.TestCase):
    def setUp(self):
        self.config = replace(tiny_config(), text_pooling='mean-residual')
        torch.manual_seed(4)
        self.reference = PostaMulti(self.config, dropout=0)
        self.recomputed = PostaMulti(self.config, dropout=0)
        self.recomputed.load_state_dict(self.reference.state_dict())

    def test_cached_gradients_match_joint_235m_architecture(self):
        pairs = fixture()
        ref = self.reference
        ref.train()
        ref.zero_grad()
        entries = []
        for role in range(3):
            entries.append(torch.cat([
                ref.encode_text(torch.tensor([triple[role]]),
                                torch.ones(1, len(triple[role]), dtype=torch.bool))
                for triple in pairs], dim=0))
        direct, _ = anti_collapse_loss(*entries)
        direct.backward()

        model = self.recomputed
        model.enable_gradient_checkpointing(True)
        model.zero_grad()
        cached_loss, diag = cached_retrieval_backward(model, pairs, 'cpu')
        self.assertAlmostEqual(cached_loss, float(direct.detach()), places=5)
        self.assertAlmostEqual(diag['pair_accuracy'], 1.0)
        for key in ('text_tokens.weight',
                    'text_encoder.layers.0.linear1.weight',
                    'text_projection.weight'):
            old = dict(ref.named_parameters())[key].grad
            new = dict(model.named_parameters())[key].grad
            self.assertIsNotNone(new, key)
            self.assertTrue(torch.allclose(old, new, atol=1e-5, rtol=2e-4), key)

    def test_evaluation_gates_healthy_vs_collapsed(self):
        metrics = evaluate_retrieval(self.reference, fixture(), 'cpu')
        self.assertEqual(metrics['samples'], 3)
        self.assertGreaterEqual(metrics['candidate_pool_size'], 6)
        self.assertTrue(0 <= metrics['recall_at_1'] <= 1)
        self.assertFalse(better_candidate(
            {'pair_accuracy': .44, 'mean_positive_minus_negative': -.0002,
             'query_interexample_cosine': .998,
             'document_interexample_cosine': .999,
             'mean_reciprocal_rank': .1}, None))
        self.assertTrue(better_candidate({
            'pair_accuracy': .8, 'mean_positive_minus_negative': .025,
            'query_interexample_cosine': .92,
            'document_interexample_cosine': .93,
            'mean_reciprocal_rank': .3}, None))

    def test_projection_warmup_preserves_pretrained_text_encoder(self):
        model = self.recomputed
        phase = configure_trainable_stage(model, 1, projection_only_steps=5)
        self.assertEqual(phase, 'projection-only')
        self.assertTrue(model.text_projection.weight.requires_grad)
        self.assertFalse(model.text_tokens.weight.requires_grad)
        self.assertFalse(model.text_encoder.layers[0].linear1.weight.requires_grad)
        model.zero_grad()
        cached_retrieval_backward(model, fixture(), 'cpu')
        self.assertIsNotNone(model.text_projection.weight.grad)
        self.assertIsNone(model.text_tokens.weight.grad)
        model.zero_grad()
        phase = configure_trainable_stage(model, 6, projection_only_steps=5)
        self.assertEqual(phase, 'text-finetune')
        self.assertTrue(model.text_tokens.weight.requires_grad)
        cached_retrieval_backward(model, fixture(), 'cpu')
        self.assertIsNotNone(model.text_tokens.weight.grad)
        with self.assertRaises(ValueError):
            configure_trainable_stage(model, 0, projection_only_steps=5)

    def test_positive_only_gradient_cache_matches_full_autograd(self):
        samples = [(q, p) for q, p, _ in fixture()]
        ref = self.reference
        ref.train()
        ref.zero_grad()
        qv = torch.cat([ref.encode_text(torch.tensor([q]),
                           torch.ones(1, len(q), dtype=torch.bool))
                        for q, _ in samples])
        pv = torch.cat([ref.encode_text(torch.tensor([p]),
                           torch.ones(1, len(p), dtype=torch.bool))
                        for _, p in samples])
        direct, _ = positive_only_alignment_loss(qv, pv)
        (direct * 0.3).backward()

        test = self.recomputed
        test.enable_gradient_checkpointing(True)
        test.zero_grad()
        objective, diag = cached_positive_only_backward(
            test, samples, 'cpu', weight=0.3)
        self.assertAlmostEqual(objective, float(direct.detach()), places=5)
        self.assertLess(abs(diag['alignment_loss']), 1e-4)
        for key in ('text_tokens.weight',
                    'text_encoder.layers.0.linear1.weight',
                    'text_projection.weight'):
            a = dict(ref.named_parameters())[key].grad
            b = dict(test.named_parameters())[key].grad
            self.assertIsNotNone(b)
            self.assertTrue(torch.allclose(a, b, atol=1e-5, rtol=2e-4), key)
        with self.assertRaises(ValueError):
            cached_positive_only_backward(test, samples[:1], 'cpu')
        with self.assertRaises(ValueError):
            cached_positive_only_backward(test, samples, 'cpu', weight=0)

    def test_invalid_small_batch_refused(self):
        with self.assertRaises(ValueError):
            cached_retrieval_backward(self.reference, fixture()[:1], 'cpu')
        with self.assertRaises(ValueError):
            cached_retrieval_backward(self.reference, fixture(), 'cpu',
                                      loss_scale=float('nan'))


if __name__ == '__main__':
    unittest.main()
