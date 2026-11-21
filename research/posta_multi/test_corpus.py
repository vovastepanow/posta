import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from corpus import Corpus


class CorpusTests(unittest.TestCase):
    def test_integrity_and_split(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            record = {'kind': 'text', 'split': 'train', 'dataset_id': 'example',
                      'dataset_revision': 'v1', 'text': 'hello'}
            data = (json.dumps(record) + '\n').encode()
            path = root / 'text-train.jsonl'
            path.write_bytes(data)
            manifest = {'files': [{'path': path.name, 'sha256': hashlib.sha256(data).hexdigest()}]}
            (root / 'manifest.json').write_text(json.dumps(manifest))
            self.assertEqual(next(Corpus(root).rows('text'))['text'], 'hello')
            path.write_bytes(data + b' ')
            with self.assertRaisesRegex(ValueError, 'hash mismatch'):
                list(Corpus(root).rows('text'))
            record['split'] = 'test'
            data = (json.dumps(record) + '\n').encode()
            path.write_bytes(data)
            manifest['files'][0]['sha256'] = hashlib.sha256(data).hexdigest()
            (root / 'manifest.json').write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'split'):
                list(Corpus(root).rows('text'))

    def test_media_escape_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'manifest.json').write_text('{"files": []}')
            with self.assertRaisesRegex(ValueError, 'invalid media path'):
                Corpus(root).media_path({'media': {'path': '../other', 'sha256': 'x'}})
