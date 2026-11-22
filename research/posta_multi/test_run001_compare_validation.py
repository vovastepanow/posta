import tempfile
import unittest
from dataclasses import asdict, replace
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

import run001_compare_validation as module
from spec import Config


class CompareValidationTests(unittest.TestCase):
    def test_rejects_different_candidate_pools_and_provenance(self):
        settings = {'run_id': module.RUN_ID, 'manifest_sha256': 'corpus',
                    'tokenizer_sha256': 'token', 'config': asdict(
                        replace(Config(), text_pooling='mean-residual')),
                    'max_length': 96, 'val_pairs': 16,
                    'quarantined_research_only': True}
        state = {'format': module.STAGE_FORMAT + '-best', 'settings': settings,
                 'step': 3, 'model': {}}
        model = MagicMock()
        model.half.return_value = model
        model.cuda.return_value = model
        tokenizer = MagicMock()
        tokenizer.token_to_id.side_effect = {'[PAD]': 0, '[CLS]': 1, '[SEP]': 2}.get
        with tempfile.TemporaryDirectory() as tmp:
            tok = Path(tmp) / 'tokenizer.json'
            tok.write_text('{}')
            with patch.object(module.torch.cuda, 'is_available', return_value=True), \
                 patch.object(module.torch.cuda, 'set_per_process_memory_fraction'), \
                 patch.object(module.torch, 'set_num_threads'), \
                 patch.object(module, 'Corpus', return_value=SimpleNamespace(manifest_hash='corpus')), \
                 patch.object(module, 'curate_retrieval_pairs', return_value=([('q', 'p', 'n')], 'pairs', {})), \
                 patch.object(module.hashlib, 'sha256') as digest, \
                 patch.object(module.torch, 'load', return_value=state), \
                 patch.object(module, 'Tokenizer') as tokenizer_class, \
                 patch.object(module, 'PostaMulti', return_value=model), \
                 patch.object(module, 'encode_triples', return_value=[]), \
                 patch.object(module, 'file_digest', return_value='weights'), \
                 patch.object(module, 'evaluate_retrieval', return_value={'candidate_pool_size': 3}), \
                 patch.object(module, 'score_pairs', return_value={'candidate_pool_size': 4}):
                digest.return_value.hexdigest.return_value = 'token'
                tokenizer_class.from_file.return_value = tokenizer
                with self.assertRaisesRegex(ValueError, 'candidate count'):
                    module.compare('data', tok, 'weights', 16)
                settings['manifest_sha256'] = 'different'
                with self.assertRaisesRegex(ValueError, 'does not match'):
                    module.compare('data', tok, 'weights', 16)


if __name__ == '__main__':
    unittest.main()
