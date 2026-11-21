"""Isolated small checks for the 235M Run 001 MLM data objective."""
import math
import unittest

import torch

from run001_text import masked_batch, mlm_step_loss, schedule, stratified_validation
from model import PostaMulti
from spec import Config
from synthetic_train import tiny_config
from dataclasses import replace


class Run001TextTests(unittest.TestCase):
    def test_mask_never_targets_cls_sep_and_is_reproducible(self):
        ids = (1, 10, 11, 12, 13, 14, 2)
        a = torch.Generator().manual_seed(44)
        b = torch.Generator().manual_seed(44)
        first = masked_batch(ids, a, max_masked=3)
        second = masked_batch(ids, b, max_masked=3)
        self.assertTrue(all(torch.equal(x, y) for x, y in zip(first, second)))
        t, mask, targets = first
        self.assertFalse(mask[0,0])
        self.assertFalse(mask[0,-1])
        self.assertGreater(int(mask.sum()), 0)
        self.assertTrue((t[mask] == 3).all())
        self.assertTrue((targets != 3).all())

    def test_loss_produces_gradient_on_scratch_weights(self):
        cfg = replace(tiny_config(), text_pooling='mean')
        torch.manual_seed(17)
        model = PostaMulti(cfg, dropout=0)
        token_ids = tuple([1, 8, 9, 11, 2])
        gen = torch.Generator().manual_seed(88)
        value = mlm_step_loss(model, token_ids, gen, 'cpu', 2)
        self.assertTrue(torch.isfinite(value))
        value.backward()
        self.assertIsNotNone(model.text_tokens.weight.grad)
        self.assertGreater(float(model.text_tokens.weight.grad.abs().sum()), 0)

    def test_warmup_and_decay_bounded(self):
        self.assertAlmostEqual(schedule(1, 100, 0.0001, 10), 0.00001)
        self.assertAlmostEqual(schedule(10, 100, 0.0001, 10), 0.0001)
        self.assertAlmostEqual(schedule(100, 100, 0.0001, 10), 0.00001)
        with self.assertRaises(ValueError):
            schedule(5, 5, 0.1, 5)


    def test_stratified_validation_is_deterministic_and_macro_averaged(self):
        model = PostaMulti(replace(tiny_config(), text_pooling='mean'), dropout=0)
        records = [(1, 8, 9, 10, 2), (1, 7, 8, 9, 2),
                   (1, 10, 12, 15, 2), (1, 12, 11, 9, 2)]
        labels = ['text:ru', 'text:ru', 'ocr_text:en', 'ocr_text:en']
        model.train()
        first = stratified_validation(model, records, labels, 'cpu', max_masked=2)
        second = stratified_validation(model, records, labels, 'cpu', max_masked=2)
        self.assertTrue(model.training)
        self.assertEqual(first, second)
        self.assertEqual(first['evaluated_windows'], 4)
        self.assertEqual(first['groups']['text:ru']['windows'], 2)
        self.assertAlmostEqual(first['macro_mean_loss'],
            (first['groups']['text:ru']['mean_loss'] +
             first['groups']['ocr_text:en']['mean_loss'])/2, places=6)
        with self.assertRaises(ValueError):
            stratified_validation(model, records, labels[:1], 'cpu')

    def test_short_or_invalid_window_refused(self):
        g = torch.Generator().manual_seed(1)
        with self.assertRaises(ValueError):
            masked_batch((1, 4, 2), g, 8)
        with self.assertRaises(ValueError):
            masked_batch((1, 10, 11, 5), g, 8)


if __name__ == '__main__':
    unittest.main()
