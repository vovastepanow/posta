"""Offline contract tests: existing HDD inventory never reads dataset row contents."""
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from local_training_inventory import (FORMAT, build, candidate_summary,
                                       inventory_downloads, role_of)


class LocalTrainingInventoryTests(unittest.TestCase):
    def test_role_isolation(self):
        self.assertEqual(role_of('fineweb2-ru'), 'text_ru')
        self.assertEqual(role_of('mmarco-ru'), 'retrieval_rights_blocked')
        self.assertEqual(role_of('mteb-ria-eval-corpus-01'), 'evaluation_only')
        self.assertEqual(role_of('mmeb-v3-eval'), 'evaluation_only')
        self.assertEqual(role_of('nvidia-ocr-en'), 'ocr_en')

    def create_sample(self, directory):
        root = Path(directory)
        source = root / 'downloads-v0.1'
        source.mkdir()
        files = {
            'fineweb2-ru': ('parquet', b'not a real parquet'),
            'mteb-ria-eval-corpus-00': ('parquet', b'evaluation bytes'),
            'videoufo': ('tar.part', b'incomplete'),
        }
        manifest = {'records': {}}
        for name, (kind, content) in files.items():
            path = source / name / ('source.' + kind)
            path.parent.mkdir()
            path.write_bytes(content)
            if name == 'videoufo':
                path = path.with_name('source.tar')
            manifest['records'][name] = {
                'dataset_id': name,
                'status': 'downloading' if name == 'videoufo' else 'verified',
                'local_path': str(path),
                'expected_bytes': 1000 if name == 'videoufo' else len(content),
            }
        (source/'manifest-one.json').write_text(json.dumps(manifest))
        candidate = root / 'Posta-Dataset-v0.1-screened'
        candidate.mkdir()
        (candidate/'manifest.json').write_text(json.dumps({
            'status': 'QUARANTINED: needs rights review', 'accepted': 74302,
            'files': [{'path': 'text-train.jsonl'}],
            'sources': {'fineweb2-ru': {}},
            'counts': [
                {'kind':'text','split':'train','language':'ru','rows': 31000},
                {'kind':'retrieval','split':'validation','language':'en','rows': 33},
            ],
        }))
        return root

    def test_counts_and_eval_exclusion_never_grant_training_permission(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.create_sample(directory)
            with patch('local_training_inventory.parquet_footer_rows',
                       return_value=240000):
                result = build(root)
            self.assertEqual(result['format'], FORMAT)
            self.assertFalse(result['training_started'])
            self.assertEqual(result['new_files_downloaded'], 0)
            self.assertFalse(result['screened_candidate']['approved_for_training'])
            self.assertEqual(result['screened_candidate']['accepted_records'], 74302)
            values = {entry['id']: entry
                      for entry in result['existing_downloads']['files']}
            self.assertEqual(values['fineweb2-ru']['parquet_footer_rows'], 240000)
            self.assertEqual(values['fineweb2-ru']['state'], 'verified_on_manifest')
            self.assertEqual(values['mteb-ria-eval-corpus-00']['role'], 'evaluation_only')
            self.assertEqual(values['videoufo']['state'], 'partial')
            self.assertEqual(values['videoufo']['stored_bytes'], len(b'incomplete'))
            self.assertEqual(result['existing_downloads']['totals']['parquet_rows_candidate_sources'],
                             240000)
            self.assertEqual(result['existing_downloads']['totals']['parquet_rows_all_roles'],
                             480000)
            self.assertTrue(any(x['directory'] == 'downloads-v0.1'
                                for x in result['top_level_storage']))

    def test_manifest_must_not_read_untrusted_external_file(self):
        with tempfile.TemporaryDirectory() as directory, tempfile.TemporaryDirectory() as outside:
            root = self.create_sample(directory)
            p = root/'downloads-v0.1'/'manifest-one.json'
            data = json.loads(p.read_text())
            data['records']['fineweb2-ru']['local_path'] = str(Path(outside)/'secret.parquet')
            (Path(outside)/'secret.parquet').write_bytes(b'private')
            p.write_text(json.dumps(data))
            with patch('local_training_inventory.parquet_footer_rows') as footer:
                report = inventory_downloads(root)
            rows = {entry['id']: entry for entry in report['files']}
            self.assertEqual(rows['fineweb2-ru']['state'],
                             'inventory_error_ValueError')
            footer.assert_called_once()  # Only the other allowed local parquet.

    def test_missing_dirs_are_aggregate_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertEqual(candidate_summary(root), {'manifest_exists': False})
            self.assertFalse(inventory_downloads(root)['directory_exists'])


if __name__ == '__main__':
    unittest.main()
