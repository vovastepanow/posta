"""Offline integrity and split checks for local candidate output."""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from verify_candidate import verify


class CandidateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        (self.root / 'media').mkdir()
        self.row = {'dataset_id': 'source', 'dataset_revision': 'rev', 'source_file_sha256': 'sha',
                    'source_file': 'train.parquet', 'record_id': '1', 'kind': 'text', 'language': 'ru',
                    'text': 'текст документа', 'positive': '', 'negatives': [], 'media_sha256': '',
                    'group_id': 'one', 'split': 'train', 'training_approved': False}
        self.write_files([self.row])

    def tearDown(self):
        self.temp.cleanup()

    def write_files(self, rows):
        grouped = {}
        for r in rows:
            grouped.setdefault((r['kind'], r['split']), []).append(r)
        files, counts = [], []
        for (kind, split), group in grouped.items():
            name = kind + '-' + split + '.jsonl'
            data = ''.join(json.dumps(r, ensure_ascii=False) + '\n' for r in group).encode()
            (self.root / name).write_bytes(data)
            files.append({'path': name, 'sha256': hashlib.sha256(data).hexdigest(), 'rows': len(group)})
            counts.append({'kind': kind, 'split': split, 'language': 'ru', 'rows': len(group)})
        manifest = {'sources': {'source': {'revision': 'rev', 'expected_sha256': 'sha',
                    'source_path': 'train.parquet'}}, 'files': files, 'counts': counts, 'accepted': len(rows)}
        (self.root / 'manifest.json').write_text(json.dumps(manifest))

    def test_detect_tampering(self):
        self.assertEqual(verify(self.root)['records'], 1)
        (self.root / 'text-train.jsonl').write_text('broken')
        with self.assertRaisesRegex(ValueError, 'hash'):
            verify(self.root)

    def test_detect_cross_split_shared_query_or_passage(self):
        second = dict(self.row, record_id='2', kind='retrieval', text='другой запрос',
                      positive=self.row['text'], group_id='two', split='test')
        self.write_files([self.row, second])
        with self.assertRaisesRegex(ValueError, 'crosses splits'):
            verify(self.root)

    def test_unapproved_source_marker_required(self):
        self.row['training_approved'] = True
        self.write_files([self.row])
        with self.assertRaisesRegex(ValueError, 'quarantine'):
            verify(self.root)

try:
    from prepare_local_shards import split_without_negative_leakage
    HAVE_SHARD_DEPS = True
except ImportError:
    HAVE_SHARD_DEPS = False


@unittest.skipUnless(HAVE_SHARD_DEPS, 'pyarrow/Pillow not installed')
class NegativeIsolationTests(unittest.TestCase):
    def test_shared_negative_is_retained_in_only_one_split(self):
        from prepare import normalize
        rows = [normalize({'dataset_id': 'x', 'record_id': str(i), 'kind': 'retrieval',
                           'language': 'en', 'text': f'query number {i}',
                           'positive': f'document number {i}',
                           'negatives': ['one shared negative document']}) for i in range(100)]
        assignments, dropped = split_without_negative_leakage(rows)
        self.assertGreater(len(set(assignments.values())), 1)
        self.assertGreater(dropped, 0)
        splits = {assignments[i] for i, row in enumerate(rows) if row['negatives']}
        self.assertEqual(len(splits), 1)


if __name__ == '__main__':
    unittest.main()
