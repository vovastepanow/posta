"""No GPU or external dataset needed to gate retrieval-v9 regression."""
import io
import json
import tempfile
import unittest
from pathlib import Path

from checkpoint_decision import analyze, training_source


METRICS = dict(pair_accuracy=0.8125, recall_at_1=0.1875,
               recall_at_5=0.5, recall_at_10=0.625,
               mean_reciprocal_rank=0.3469)


def record(name, task, data):
    return {'checkpoint': name, 'task': task, 'status': 'ok', 'metrics': data}


def reports(v8_accuracy=0.85, v9_accuracy=0.4375,
            v8_mrr=0.4, v9_mrr=0.1352):
    common = {'samples': 16, 'validation_pairs_sha256': 'same-16-selected-rows'}
    v1 = dict(METRICS, **common)
    v8 = dict(METRICS, pair_accuracy=v8_accuracy, mean_reciprocal_rank=v8_mrr, **common)
    v9 = dict(METRICS, pair_accuracy=v9_accuracy, mean_reciprocal_rank=v9_mrr,
              recall_at_1=0.0625, **common)
    first = {
        'format': 'posta-multi-checkpoint-comparison-v1',
        'dataset': '/hdd/dataset', 'tokenizer': '/hdd/tok.json',
        'tasks': ['text', 'retrieval'],
        'checkpoints': ['retrieval-v1', 'text-v8'],
        'results': [
            record('retrieval-v1', 'text', {'checkpoint_step': 32}),
            record('retrieval-v1', 'retrieval', v1),
            record('text-v8', 'text', {'checkpoint_step': 128}),
            record('text-v8', 'retrieval', v8),
        ],
    }
    second = {
        'format': first['format'], 'dataset': first['dataset'],
        'tokenizer': first['tokenizer'], 'tasks': ['text', 'retrieval'],
        'checkpoints': ['retrieval-v1', 'retrieval-adam-v9'],
        'results': [
            record('retrieval-v1', 'text', {'checkpoint_step': 32}),
            record('retrieval-v1', 'retrieval', dict(v1)),
            record('retrieval-adam-v9', 'text', {'checkpoint_step': 64}),
            record('retrieval-adam-v9', 'retrieval', v9),
        ],
    }
    return first, second


class CheckpointDecisionTests(unittest.TestCase):
    def test_v9_regression_against_actual_v8_initialization(self):
        first, second = reports()
        result = analyze(first, second, 'text-v8')
        self.assertEqual(result['verdict'], 'validation_regression')
        self.assertAlmostEqual(result['delta_v9_vs_initial_source']['pair_accuracy'], -.4125)
        self.assertEqual(result['training_source'], 'text-v8')
        self.assertFalse(result['eligible_for_production'])
        self.assertFalse(result['further_training_automatically_authorized'])

    def test_different_training_source_changes_baseline(self):
        first, second = reports()
        result = analyze(first, second, 'retrieval-v1')
        self.assertAlmostEqual(result['delta_v9_vs_initial_source']['pair_accuracy'], -.375)
        self.assertEqual(result['verdict'], 'validation_regression')

    def test_no_regression_never_promotes_on_tiny_validation(self):
        first, second = reports(v9_accuracy=0.95, v9_mrr=0.9)
        second['results'][-1]['metrics']['recall_at_1'] = 0.5
        result = analyze(first, second, 'text-v8')
        self.assertEqual(result['verdict'], 'tentative_local_improvement_only')
        self.assertFalse(result['eligible_for_production'])

    def test_rejects_incomparable_or_corrupted_reports(self):
        for problem in ('wrong-data', 'other-sample', 'anchor', 'nan',
                        'missing', 'warning', 'duplicate', 'wrong-task'):
            a, b = reports()
            if problem == 'wrong-data':
                b['dataset'] = '/unrelated/dataset'
            if problem == 'other-sample':
                b['results'][-1]['metrics']['validation_pairs_sha256'] = 'different'
            if problem == 'anchor':
                b['results'][1]['metrics']['pair_accuracy'] = 0.5
            if problem == 'nan':
                b['results'][-1]['metrics']['mean_reciprocal_rank'] = float('nan')
            if problem == 'missing':
                b['results'].pop()
            if problem == 'warning':
                b['warnings'] = ['incomparable']
            if problem == 'duplicate':
                b['results'][2] = b['results'][1]
            if problem == 'wrong-task':
                b['tasks'] = ['retrieval-test', 'text']
            with self.subTest(problem=problem), self.assertRaises(ValueError):
                analyze(a, b, 'text-v8')

    def test_log_parser_requires_unique_source(self):
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / 'train.log'
            path.write_text('training started\n{"stage":"retrieval-adam-v9","source":"text-v8"}\n'
                            '{"saved_step":64}\n', encoding='utf-8')
            self.assertEqual(training_source(path), 'text-v8')
            path.write_text('{"stage":"retrieval-adam-v9","source":"text-v8"}\n'
                            '{"stage":"retrieval-adam-v9","source":"retrieval-v1"}\n')
            with self.assertRaisesRegex(ValueError, 'conflicting'):
                training_source(path)
            path.write_text('{"saved_step":64}\n')
            with self.assertRaisesRegex(ValueError, 'does not establish'):
                training_source(path)


if __name__ == '__main__':
    unittest.main()
