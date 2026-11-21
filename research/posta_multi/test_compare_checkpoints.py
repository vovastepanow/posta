"""No GPU/data required; checks the read-only evaluation coordinator."""
import json
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from compare_checkpoints import (
    CHECKPOINTS, atomic_report, evaluation_command, parse_evaluation_output,
    run, summarize,
)


class CompareCheckpointsTests(unittest.TestCase):
    def test_command_calls_only_known_validation_scripts(self):
        command = evaluation_command('retrieval', Path('/hdd/r.pt'), '/hdd/data',
                                     '/hdd/t.json', python='/usr/bin/python3')
        self.assertEqual(command[0], '/usr/bin/python3')
        self.assertTrue(command[1].endswith('/eval_retrieval.py'))
        self.assertEqual(command[-2:], ['--samples', '16'])
        self.assertNotIn('--split', command)
        with self.assertRaises(ValueError):
            evaluation_command('training', 'x', 'y', 'z')

    def test_extracts_evaluation_json_after_log_lines(self):
        item = parse_evaluation_output('loading\n{"checkpoint_step": 128, "pair_accuracy": 0.5}\n')
        self.assertEqual(item['checkpoint_step'], 128)
        with self.assertRaises(ValueError):
            parse_evaluation_output('missing output')

    def test_detects_mismatched_validation_selection(self):
        rows = [
            {'checkpoint': name, 'task': 'retrieval', 'status': 'ok',
             'metrics': {'validation_pairs_sha256': digest}}
            for name, digest in (('a', '123'), ('b', '456'))
        ]
        self.assertTrue(summarize(rows))
        rows[1]['metrics']['validation_pairs_sha256'] = '123'
        self.assertEqual(summarize(rows), [])

    def test_mock_evaluation_writes_report_only_and_does_not_train(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            (root / 'corpus').mkdir()
            (root / 'corpus' / 'manifest.json').write_text('{}')
            (root / 'tokenizer.json').write_text('{}')
            (root / CHECKPOINTS['text-v8']).write_bytes(b'test-placeholder')
            args = SimpleNamespace(
                dataset=str(root / 'corpus'), tokenizer=str(root / 'tokenizer.json'),
                checkpoint_dir=str(root), checkpoints=['text-v8'], tasks=['retrieval'],
                timeout=30, output=str(root / 'results.json'),
            )
            completed = SimpleNamespace(returncode=0, stderr='',
                                        stdout='{"checkpoint_step":128,"pair_accuracy":0.5}\n')
            with patch('compare_checkpoints.subprocess.run', return_value=completed) as mock:
                self.assertEqual(run(args), 0)
                mock.assert_called_once()
                command = mock.call_args.args[0]
                self.assertTrue(command[1].endswith('/eval_retrieval.py'))
                self.assertNotIn('train', Path(command[1]).name)
            payload = json.loads((root / 'results.json').read_text())
            self.assertEqual(payload['results'][0]['metrics']['pair_accuracy'], 0.5)

    def test_fails_without_hdd_files(self):
        with tempfile.TemporaryDirectory() as root:
            args = SimpleNamespace(
                dataset=root, tokenizer=root + '/missing.json',
                checkpoint_dir=root, checkpoints=['text-v8'], tasks=['text'],
                timeout=30, output=root + '/report.json')
            with self.assertRaises(FileNotFoundError):
                run(args)

    def test_atomic_report_is_valid_json(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'report.json'
            atomic_report(path, {'hello': 'мир'})
            self.assertEqual(json.loads(path.read_text()), {'hello': 'мир'})


if __name__ == '__main__':
    unittest.main()
