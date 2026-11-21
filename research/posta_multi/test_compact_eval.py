"""Pure CPU checks of compact test-only checkpoint verification."""
import unittest
from dataclasses import asdict

from compact import CompactConfig
from compact_eval import check_provenance


class CompactEvaluationTests(unittest.TestCase):
    def test_accepts_matching_checkpoint(self):
        config = CompactConfig(vocab_size=320, width=32, layers=1, heads=4,
                               ff_width=64, max_length=16, output_dim=16)
        example = {'format':'posta-compact-best-v1', 'step':80,
                   'settings': {'dataset_sha256':'manifest', 'tokenizer_sha256':'tokenizer',
                                'config': asdict(config)}}
        self.assertEqual(check_provenance(example, 'manifest', 'tokenizer'), config)
        with self.assertRaisesRegex(ValueError, 'provenance'):
            check_provenance(example, 'different', 'tokenizer')
        with self.assertRaisesRegex(ValueError, 'provenance'):
            check_provenance(example, 'manifest', 'different')
        example['format'] = 'posta-retrieval-v1'
        with self.assertRaisesRegex(ValueError, 'expected'):
            check_provenance(example, 'manifest', 'tokenizer')

    def test_rejects_incomplete_or_incompatible_models(self):
        with self.assertRaises(ValueError):
            check_provenance({}, 'a', 'b')
        with self.assertRaises(ValueError):
            check_provenance({'format':'posta-compact-best-v1'}, 'a', 'b')
        with self.assertRaises(ValueError):
            check_provenance({'format':'posta-compact-best-v1',
                              'step':0, 'settings':{
                                  'dataset_sha256':'a', 'tokenizer_sha256':'b',
                                  'config': asdict(CompactConfig())}}, 'a', 'b')


if __name__ == '__main__':
    unittest.main()
