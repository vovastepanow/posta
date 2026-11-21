import hashlib
import json
import tempfile
import unittest
from pathlib import Path
import torch
from train_text import REVISION, run


class TextTrainingTests(unittest.TestCase):
    def test_resume_and_changed_data_refusal(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            records = [{'dataset_id': 'nvidia-ocr-ru', 'dataset_revision': REVISION,
                        'kind': 'ocr_text', 'split': 'train', 'text': text, 'ocr': text}
                       for text in ('первый пример', 'another example')]
            data = ''.join(json.dumps(row, ensure_ascii=False)+'\n' for row in records).encode()
            (root/'ocr_text-train.jsonl').write_bytes(data)
            manifest = {'files': [{'path': 'ocr_text-train.jsonl', 'sha256': hashlib.sha256(data).hexdigest()}]}
            (root/'manifest.json').write_text(json.dumps(manifest))
            full, resumed = root/'full.pt', root/'resume.pt'
            run(root, full, 4, samples=2)
            run(root, resumed, 2, samples=2)
            run(root, resumed, 4, samples=2, resume=True)
            a = torch.load(full, map_location='cpu', weights_only=True)
            b = torch.load(resumed, map_location='cpu', weights_only=True)
            self.assertEqual(b['step'], 4)
            self.assertTrue(torch.equal(a['cpu_rng'], b['cpu_rng']))
            for key in a['model']:
                self.assertTrue(torch.equal(a['model'][key], b['model'][key]), key)
            warm = root/'warm.pt'
            run(root, warm, 1, samples=1, warm_start=full)
            warm_state = torch.load(warm, map_location='cpu', weights_only=True)
            self.assertEqual(warm_state['origin']['step'], 4)
            self.assertEqual(len(warm_state['origin']['checkpoint_sha256']), 64)
            run(root, warm, 2, samples=1, resume=True)
            with self.assertRaises(ValueError):
                run(root, resumed, 5, samples=1, resume=True)
            with self.assertRaises(FileExistsError):
                run(root, full, 1, samples=2)
