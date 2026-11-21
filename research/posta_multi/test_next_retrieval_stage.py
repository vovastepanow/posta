"""CPU-only tests of next-stage gating and command construction."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

from next_retrieval_stage import choose_source, run


def report(v8_pair=0.8125, v8_mrr=0.3, old_pair=0.8125, old_mrr=0.25):
    rows = []
    for name, pair, mrr in (('retrieval-v1', old_pair, old_mrr), ('text-v8', v8_pair, v8_mrr)):
        rows.append({'checkpoint': name, 'task': 'text', 'status': 'ok',
                     'metrics': {'checkpoint_step': 128, 'local_validation': {}}})
        rows.append({'checkpoint': name, 'task': 'retrieval', 'status': 'ok',
                     'metrics': {'checkpoint_step': 128, 'samples': 16,
                                 'validation_pairs_sha256': 'sample-hash',
                                 'pair_accuracy': pair, 'mean_reciprocal_rank': mrr}})
    return {'format': 'posta-multi-checkpoint-comparison-v1', 'warnings': [], 'results': rows}


class NextRetrievalStageTests(unittest.TestCase):
    def test_source_is_chosen_only_if_retrieval_does_not_regress(self):
        self.assertEqual(choose_source(report()), 'text-v8')
        self.assertEqual(choose_source(report(v8_pair=0.75)), 'retrieval-v1')
        self.assertEqual(choose_source(report(v8_mrr=0.2)), 'retrieval-v1')

    def test_refuses_missing_warnings_incompatible_samples_and_missing_metrics(self):
        for mutate in ('warning', 'missing', 'different', 'badscore'):
            payload = report()
            if mutate == 'warning':
                payload['warnings'] = ['incomparable']
            elif mutate == 'missing':
                payload['results'].pop()
            elif mutate == 'different':
                payload['results'][-1]['metrics']['validation_pairs_sha256'] = 'other'
            elif mutate == 'badscore':
                payload['results'][-1]['metrics']['pair_accuracy'] = float('nan')
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                choose_source(payload)

    def test_bounded_plan_never_overwrites_existing_or_calls_training_without_checks(self):
        from compare_checkpoints import CHECKPOINTS
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            dataset = root / 'data'
            dataset.mkdir()
            (dataset / 'manifest.json').write_text('{}')
            token = root / 'tokenizer.json'
            token.write_text('{}')
            source = root / CHECKPOINTS['text-v8']
            source.write_bytes(b'source-stub')
            path = root / 'comparison.json'
            verified_report = report()
            verified_report.update({
                'dataset': str(dataset),
                'tokenizer': str(token),
                'checkpoints': ['retrieval-v1', 'text-v8'],
                'tasks': ['text', 'retrieval'],
            })
            path.write_text(json.dumps(verified_report))
            destination = root / CHECKPOINTS['retrieval-adam-v9']
            usage = SimpleNamespace(free=12*1024**3)
            with patch('next_retrieval_stage.shutil.disk_usage', return_value=usage), \
                 patch('next_retrieval_stage.subprocess.run') as call:
                run(root, path, dataset, token, '/path/to/python')
                self.assertEqual(call.call_count, 2)
                training_cmd = call.call_args_list[0].args[0]
                self.assertTrue(training_cmd[1].endswith('train_retrieval.py'))
                self.assertIn('--text-only-adam', training_cmd)
                self.assertIn('--sample-hash', training_cmd)
                self.assertEqual(training_cmd[training_cmd.index('--steps')+1], '64')
                self.assertEqual(training_cmd[training_cmd.index('--samples')+1], '512')
                self.assertIn(str(destination), training_cmd)
                # Wrong data path must not ever authorize a training subprocess.
                wrong_report = dict(verified_report, tokenizer='/unrelated/tokenizer.json')
                path.write_text(json.dumps(wrong_report))
                with self.assertRaisesRegex(ValueError, 'does not match'):
                    run(root, path, dataset, token, '/path/to/python')
                self.assertEqual(call.call_count, 2)
                path.write_text(json.dumps(verified_report))
                destination.write_bytes(b'preserve-existing')
                with self.assertRaises(FileExistsError):
                    run(root, path, dataset, token, '/path/to/python')
                self.assertEqual(destination.read_bytes(), b'preserve-existing')


if __name__ == '__main__':
    unittest.main()
