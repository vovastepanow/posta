import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from dataset import tokenized_retrieval_rows, verified_retrieval_rows
from tokenizer import CLS, SEP, encode, decode


class InputTests(unittest.TestCase):
    def test_multilingual_and_character_safe_truncation(self):
        for text in ('Привет, мир!', 'hello 🌍', '你好', ''):
            self.assertEqual(decode(encode(text)), text)
        tokens = encode('a🌍b', max_length=6)
        self.assertEqual(tokens[0], CLS)
        self.assertEqual(tokens[-1], SEP)
        self.assertEqual(decode(tokens), 'a')

    def test_gate_integrity_and_train_only(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            row = {'kind': 'retrieval', 'split': 'train', 'training_approved': True,
                   'text': 'query', 'positive': 'answer', 'dataset_id': 'example', 'dataset_revision': 'v1'}
            data = (json.dumps(row) + '\n').encode()
            (root / 'retrieval-train.jsonl').write_bytes(data)
            manifest = {'status': 'quarantined', 'files': [{'path': 'retrieval-train.jsonl', 'sha256': hashlib.sha256(data).hexdigest()}]}
            path = root / 'manifest.json'
            path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'not approved'):
                list(verified_retrieval_rows(root))
            manifest['status'] = 'approved_for_training'
            path.write_text(json.dumps(manifest))
            self.assertEqual(len(list(verified_retrieval_rows(root))), 1)
            tokenized = list(tokenized_retrieval_rows(root))[0]
            self.assertEqual(decode(tokenized['query_ids']), 'query')
            self.assertEqual(decode(tokenized['positive_ids']), 'answer')
            (root / 'retrieval-train.jsonl').write_bytes(data + b' ')
            with self.assertRaisesRegex(ValueError, 'hash mismatch'):
                list(verified_retrieval_rows(root))
            row['split'] = 'test'
            data = (json.dumps(row) + '\n').encode()
            (root / 'retrieval-train.jsonl').write_bytes(data)
            manifest['files'][0]['sha256'] = hashlib.sha256(data).hexdigest()
            path.write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, 'not an approved'):
                list(verified_retrieval_rows(root))
