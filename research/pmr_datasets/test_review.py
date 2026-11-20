"""Test review pilot integrity and quarantine invariants without network."""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from build_review import build
from verify_review import verify


class ReviewPilotTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.source = self.root / 'source'
        self.source.mkdir()
        self.output = self.root / 'out'
        self.row = {'dataset_id': 'x', 'dataset_revision_observed': 'abc', 'record_id': '1',
                    'row_index': 0, 'kind': 'text', 'language': 'ru', 'text': 'текст документа',
                    'group_id': '1', 'source_split': 'train', 'training_approved': False,
                    'annotation_origin': 'upstream preview'}
        self.write_source()

    def tearDown(self):
        self.temp.cleanup()

    def write_source(self):
        data = (json.dumps(self.row, ensure_ascii=False) + '\n').encode()
        (self.source / 'x.jsonl').write_bytes(data)
        (self.source / 'manifest.json').write_text(json.dumps({'sources': [{
            'id': 'x', 'observed_head': 'abc', 'split': 'train', 'file': 'x.jsonl',
            'sha256': hashlib.sha256(data).hexdigest()}]}))

    def test_build_and_verify_then_detect_tampering(self):
        report = build(self.source, self.output)
        self.assertEqual(report['accepted_rows'], 1)
        self.assertEqual(verify(self.output)['rows'], 1)
        (self.output / report['files'][0]['file']).write_text('tampered')
        with self.assertRaisesRegex(ValueError, 'SHA256'):
            verify(self.output)

    def test_source_mismatch_rejected(self):
        self.row['dataset_revision_observed'] = 'another'
        self.write_source()
        report = build(self.source, self.output)
        self.assertEqual(report['accepted_rows'], 0)
        self.assertIn('provenance', report['rejected'][0]['reason'])

    def test_review_requires_explicit_quarantine(self):
        self.row['training_approved'] = True
        self.write_source()
        report = build(self.source, self.output)
        self.assertEqual(report['accepted_rows'], 0)
        self.assertIn('unapproved', report['rejected'][0]['reason'])


if __name__ == '__main__':
    unittest.main()
