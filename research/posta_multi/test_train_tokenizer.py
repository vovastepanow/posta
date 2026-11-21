import hashlib
import json
import tempfile
import unittest
from pathlib import Path

try:
    from tokenizers import Tokenizer
    from train_tokenizer import train
    from train_text import REVISION
    HAVE_TOKENIZERS = True
except ImportError:
    HAVE_TOKENIZERS = False


@unittest.skipUnless(HAVE_TOKENIZERS, 'tokenizers dependency not installed')
class TokenizerTrainingTests(unittest.TestCase):
    def test_balance_roundtrip_and_provenance(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            rows = []
            for source, text in [('nvidia-ocr-ru', 'Привет, мир!'), ('nvidia-ocr-en', 'Hello, world!')]:
                rows.append({'dataset_id': source, 'dataset_revision': REVISION, 'kind': 'ocr_text',
                             'split': 'train', 'text': text, 'ocr': text})
            data = ''.join(json.dumps(row, ensure_ascii=False)+'\n' for row in rows).encode()
            (root/'ocr_text-train.jsonl').write_bytes(data)
            (root/'manifest.json').write_text(json.dumps({'files': [{'path': 'ocr_text-train.jsonl',
                                                                      'sha256': hashlib.sha256(data).hexdigest()}]}))
            metadata = train(root, root/'tokenizer', 1, 300)
            self.assertEqual(metadata['counts'], {'ru': 1, 'en': 1})
            tokenizer = Tokenizer.from_file(str(root/'tokenizer'/'tokenizer.json'))
            for text in ('Привет, мир!', 'Hello, world!', '你好 🌍'):
                encoded = tokenizer.encode(text)
                self.assertEqual(encoded.ids[0], 1)
                self.assertEqual(encoded.ids[-1], 2)
                self.assertEqual(tokenizer.decode(encoded.ids), text)
            self.assertEqual(metadata['tokenizer_sha256'], hashlib.sha256((root/'tokenizer'/'tokenizer.json').read_bytes()).hexdigest())
            with self.assertRaises(FileExistsError):
                train(root, root/'tokenizer', 1, 300)
