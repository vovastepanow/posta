"""Synthetic-only Run 001 tests. No CUDA or HDD needed."""
import unittest
from dataclasses import replace

import torch

from model import PostaMulti
from run001_smoke import (RUN_ID, TEXT_NAMES, build_model, synthetic_masked_step,
                          trainable_text_parameters)
from spec import Config, parameter_counts
from synthetic_train import tiny_config


class Run001SmokeTests(unittest.TestCase):
    def test_full_architecture_stays_235m(self):
        c = replace(Config(), text_pooling='mean')
        self.assertEqual(parameter_counts(c)['total'], 235179777)
        self.assertEqual(RUN_ID, 'posta-multi-235m-run-001')

    def test_freezes_nontext_parameters(self):
        c = replace(tiny_config(), text_pooling='mean')
        model = build_model(c, 'cpu')
        active = trainable_text_parameters(model)
        self.assertGreater(len(active), 0)
        self.assertTrue(all(param.requires_grad for param in active))
        for name, param in model.named_parameters():
            expected = any(name == p or name.startswith(p + '.') for p in TEXT_NAMES)
            self.assertEqual(param.requires_grad, expected, name)

    def test_actual_cpu_training_update(self):
        c = replace(tiny_config(), text_pooling='mean')
        model = build_model(c, 'cpu')
        optimizer = torch.optim.AdamW((p for p in model.parameters()
                                       if p.requires_grad), lr=0.003)
        result = synthetic_masked_step(model, optimizer, 'cpu', length=8,
                                       max_masked=3)
        self.assertGreater(result['nonzero_gradient_tensors'], 2)
        self.assertTrue(result['updated_text_embeddings'])
        self.assertGreater(result['loss'], 0)

    def test_bad_length_fails_closed(self):
        model = build_model(replace(tiny_config(), text_pooling='mean'), 'cpu')
        opt = torch.optim.AdamW((p for p in model.parameters() if p.requires_grad), lr=0.001)
        with self.assertRaises(ValueError):
            synthetic_masked_step(model, opt, 'cpu', length=2)
        with self.assertRaises(ValueError):
            synthetic_masked_step(model, opt, 'cpu', length=100000)
        with self.assertRaises(ValueError):
            synthetic_masked_step(model, opt, 'cpu', length=8, max_masked=7)
        with self.assertRaises(ValueError):
            synthetic_masked_step(model, opt, 'cpu', length=8, max_masked=0)


if __name__ == '__main__':
    unittest.main()
