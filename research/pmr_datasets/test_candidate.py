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

    def test_shared_source_url_cannot_cross_splits(self):
        self.row['source_url'] = 'https://example.test/article'
        second = dict(self.row, record_id='2', text='другая версия текста', group_id='two', split='test')
        self.write_files([self.row, second])
        with self.assertRaisesRegex(ValueError, 'crosses splits'):
            verify(self.root)

    def test_unapproved_source_marker_required(self):
        self.row['training_approved'] = True
        self.write_files([self.row])
        with self.assertRaisesRegex(ValueError, 'quarantine'):
            verify(self.root)

try:
    from prepare_local_shards import build, ensure_speech_holdouts, load_sources, split_without_negative_leakage
    HAVE_SHARD_DEPS = True
except ImportError:
    HAVE_SHARD_DEPS = False


@unittest.skipUnless(HAVE_SHARD_DEPS, 'pyarrow/Pillow not installed')
class NegativeIsolationTests(unittest.TestCase):
    def test_build_from_verified_parquet_source(self):
        import pyarrow as pa
        import pyarrow.parquet as pq
        import hashlib
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);src=root/'src';src.mkdir();parquet=src/'text.parquet'
            pq.write_table(pa.table({'text':['a valid sample document'], 'id':['doc1'],
                                     'url':['https://example.test/doc'], 'language_score':[0.99]}),parquet)
            data=parquet.read_bytes();entry={'status':'verified','local_path':str(parquet),
                'expected_bytes':len(data),'expected_sha256':hashlib.sha256(data).hexdigest(),
                'revision':'rev','source_path':'train.parquet','repository':'example/repo'}
            (src/'manifest-a.json').write_text(json.dumps({'records':{'fineweb-en':entry}}))
            result=build(src,root/'out',{'fineweb-en':1},{'fineweb-en'})
            self.assertEqual(result['accepted'],1)
            self.assertEqual(verify(root/'out')['records'],1)

    def test_selected_source_must_be_verified_and_present(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaisesRegex(ValueError, 'requested verified sources missing'):
                load_sources(Path(tmp), {'fineweb-en'})

    def test_speaker_holdouts_keep_groups_separate(self):
        from prepare import normalize
        import hashlib
        rows = [normalize({'dataset_id':'audio','record_id':str(i),'kind':'speech_text',
                           'language':'en','text':f'speech transcript {i}',
                           'group_id':f'speaker:{i}',
                           'media_sha256':hashlib.sha256(str(i).encode()).hexdigest()}) for i in range(4)]
        assignments = {i:'train' for i in range(len(rows))}
        moved = ensure_speech_holdouts(rows, assignments)
        self.assertEqual({x['split'] for x in moved}, {'validation','test'})
        self.assertEqual(set(assignments.values()), {'train','validation','test'})

    def test_shared_text_prevents_speaker_move(self):
        from prepare import normalize
        import hashlib
        rows = [normalize({'dataset_id':'audio','record_id':str(i),'kind':'speech_text',
                           'language':'en','text':f'speech transcript {i}',
                           'group_id':f'speaker:{i}',
                           'media_sha256':hashlib.sha256(str(i).encode()).hexdigest()}) for i in range(4)]
        rows.append(normalize({'dataset_id':'text','record_id':'x','kind':'text',
                               'language':'en','text':'speech transcript 0','group_id':'x'}))
        assignments={i:'train' for i in range(len(rows))}
        ensure_speech_holdouts(rows,assignments)
        self.assertEqual(assignments[0], 'train')
        self.assertEqual(assignments[4], 'train')

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
        first_negatives = [list(row['negatives']) for row in rows]
        repeated, dropped_again = split_without_negative_leakage(rows)
        self.assertEqual(repeated, assignments)
        self.assertEqual(dropped_again, 0)
        self.assertEqual([row['negatives'] for row in rows], first_negatives)


if __name__ == '__main__':
    unittest.main()
