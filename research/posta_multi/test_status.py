"""Pure offline checks for local progress/metrics display."""
import json
import tempfile
import unittest
from pathlib import Path

from status import JOB_REPORT_FILES, job_status, recent_stage, summarize_report


class StatusTests(unittest.TestCase):
    def test_report_paths_match_actual_evaluation_outputs(self):
        self.assertEqual(JOB_REPORT_FILES['posta-multi-retrieval-adam-v9'],
                         'posta-multi-retrieval-adam-v9-eval.json')

    def test_geometry_job_is_tracked_and_reports_numeric_only(self):
        self.assertEqual(JOB_REPORT_FILES['posta-multi-geometry-v1-v8-v9-20261121-v1'],
                         'posta-multi-geometry-v1-v8-v9-20261121-v1.json')
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = 'posta-multi-geometry-v1-v8-v9-20261121-v1'
            (root / JOB_REPORT_FILES[name]).write_text(json.dumps({
                'format': 'posta-multi-checkpoint-comparison-v1',
                'tasks': ['retrieval-health'],
                'checkpoints': ['retrieval-v1'],
                'results': [{
                    'checkpoint': 'retrieval-v1', 'task': 'retrieval-health',
                    'status': 'ok', 'metrics': {
                        'samples': 16, 'pair_accuracy': 0.8125,
                        'diagnostics': {'mean_positive_minus_negative': 0.4,
                                        'query_interexample_cosine': 0.6,
                                        'note': 'not private document content'}}}
                ]}))
            result = summarize_report(root / JOB_REPORT_FILES[name])
            self.assertEqual(result['results'][0]['diagnostics']['mean_positive_minus_negative'], 0.4)
            self.assertNotIn('note', result['results'][0]['diagnostics'])

    def test_holdout_test_report_is_detected(self):
        self.assertEqual(JOB_REPORT_FILES['posta-multi-holdout-v1-v8-v9-20261121-v1'],
                         'posta-multi-holdout-v1-v8-v9-20261121-v1.json')
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = 'posta-multi-holdout-v1-v8-v9-20261121-v1'
            (root / JOB_REPORT_FILES[name]).write_text(json.dumps({
                'format': 'posta-multi-checkpoint-comparison-v1',
                'tasks': ['retrieval-test'],
                'checkpoints': ['retrieval-v1'],
                'results': [{
                    'checkpoint': 'retrieval-v1', 'task': 'retrieval-test', 'status': 'ok',
                    'metrics': {'samples': 64, 'pair_accuracy': 0.5, 'mean_reciprocal_rank': 0.23}
                }]
            }))
            self.assertTrue(job_status(root, name)['report_exists'])
            summary = summarize_report(root / JOB_REPORT_FILES[name])
            self.assertTrue(summary['complete'])
            self.assertEqual(summary['results'][0]['samples'], 64)

    def test_completed_stage_report_summary_and_training_origin(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            stage = 'posta-multi-retrieval-adam-v9'
            (root / (stage + '.pt')).write_bytes(b'placeholder')
            (root / (stage + '.exit')).write_text('0\n')
            (root / (stage + '.log')).write_text(
                '{"stage":"retrieval-adam-v9","source":"retrieval-v1"}\n'
                '{"saved_step":64,"last_loss":0.6963787}\n'
            )
            payload = {
                'format': 'posta-multi-checkpoint-comparison-v1',
                'tasks': ['text', 'retrieval'],
                'checkpoints': ['retrieval-v1', 'retrieval-adam-v9'],
                'warnings': [],
                'results': [
                    {'checkpoint': 'retrieval-v1', 'task': 'text', 'status': 'ok',
                     'metrics': {'checkpoint_step': 32, 'local_validation': {
                         'text:ru': {'records': 8, 'masked_tokens': 16,
                                     'mean_loss': 9.4, 'token_accuracy': 0.03}}}},
                    {'checkpoint': 'retrieval-v1', 'task': 'retrieval', 'status': 'ok',
                     'metrics': {'checkpoint_step': 32, 'pair_accuracy': 0.8125,
                                 'mean_reciprocal_rank': 0.3, 'samples': 16,
                                 'validation_pairs_sha256': 'private-hash'}},
                    {'checkpoint': 'retrieval-adam-v9', 'task': 'text', 'status': 'ok',
                     'metrics': {'checkpoint_step': 64, 'local_validation': {}}},
                    {'checkpoint': 'retrieval-adam-v9', 'task': 'retrieval', 'status': 'ok',
                     'metrics': {'checkpoint_step': 64, 'pair_accuracy': 0.875,
                                 'mean_reciprocal_rank': 0.4, 'samples': 16}},
                ],
            }
            report = root / JOB_REPORT_FILES[stage]
            report.write_text(json.dumps(payload), encoding='utf-8')
            self.assertEqual(recent_stage(root, stage)['initialized_from'], 'retrieval-v1')
            self.assertEqual(recent_stage(root, stage)['latest_log_entry']['saved_step'], 64)
            job = job_status(root, stage)
            self.assertEqual(job['exit_code'], 0)
            self.assertTrue(job['report_exists'])
            self.assertTrue(job['log_exists'])
            summary = summarize_report(report)
            self.assertTrue(summary['complete'])
            self.assertEqual(summary['results'][-1]['pair_accuracy'], 0.875)
            self.assertNotIn('validation_pairs_sha256', summary['results'][1])
            self.assertEqual(summary['results'][0]['text_groups']['text:ru']['mean_loss'], 9.4)

    def test_failed_or_partial_report_visible_without_false_success(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            name = 'posta-multi-compare-v8-retrieval-20261121-v1'
            self.assertIsNone(summarize_report(root / JOB_REPORT_FILES[name]))
            self.assertFalse(job_status(root, name)['report_exists'])
            (root / JOB_REPORT_FILES[name]).write_text('{broken', encoding='utf-8')
            self.assertIn('read_error', summarize_report(root / JOB_REPORT_FILES[name]))
            (root / JOB_REPORT_FILES[name]).write_text(json.dumps({
                'format': 'posta-multi-checkpoint-comparison-v1',
                'tasks': ['retrieval'], 'checkpoints': ['a', 'b'],
                'warnings': ['incomparable validation'],
                'results': [{'checkpoint': 'a', 'task': 'retrieval', 'status': 'error',
                             'error': 'validation not available'}],
            }), encoding='utf-8')
            item = summarize_report(root / JOB_REPORT_FILES[name])
            self.assertFalse(item['complete'])
            self.assertEqual(item['warnings'], ['incomparable validation'])
            self.assertEqual(item['results'][0]['error'], 'validation not available')

    def test_wrong_report_format_is_not_summarized(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'r.json'
            path.write_text(json.dumps({'format': 'other'}))
            self.assertIn('read_error', summarize_report(path))


if __name__ == '__main__':
    unittest.main()
