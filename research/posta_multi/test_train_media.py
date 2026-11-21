import unittest
from train_media import load_rows


class CorpusFixture:
    def rows(self, kind, split):
        assert kind == 'video_text' and split == 'train'
        for text, sha in [('a', '1'), ('a', '2'), ('b', '1'), ('b', '3')]:
            yield {'dataset_id': 'v', 'dataset_revision': 'r', 'text': text, 'media': {'sha256': sha}}


class MediaTests(unittest.TestCase):
    def test_distinct_selection(self):
        rows, digest = load_rows(CorpusFixture(), 'video_text', 2)
        self.assertEqual([row['text'] for row in rows], ['a', 'b'])
        self.assertEqual(len(digest), 64)

class AudioInputTests(unittest.TestCase):
    def test_full_audio_is_not_truncated(self):
        import math
        import struct
        import tempfile
        import wave
        from pathlib import Path
        from corpus_smoke import log_mel
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'audio.wav'
            samples = [int(10000 * math.sin(2 * math.pi * 440 * i / 16000)) for i in range(16000)]
            with wave.open(str(path), 'wb') as wav:
                wav.setparams((1, 2, 16000, 0, 'NONE', 'not compressed'))
                wav.writeframes(struct.pack('<' + 'h' * len(samples), *samples))
            short = log_mel(path, duration=0.25)
            full = log_mel(path, duration=None)
            self.assertGreater(full.shape[-1], short.shape[-1] * 3)

class SegmentPoolingTests(unittest.TestCase):
    def test_all_segments_contribute_to_gradients(self):
        import torch
        from train_media import encode_complete_audio
        class FakeModel(torch.nn.Module):
            def __init__(self):
                super().__init__()
                self.weight = torch.nn.Parameter(torch.tensor([1., 2.]))
            def encode_audio(self, segment):
                mean = segment.mean(dim=(1, 2))
                return torch.stack((mean * self.weight[0], mean.square() * self.weight[1]), dim=1)
        model = FakeModel()
        mel = torch.ones(1, 2, 512, requires_grad=True)
        mel.data[:, :, 256:] = 2
        output = encode_complete_audio(model, mel)
        output[0, 0].backward()
        self.assertGreater(mel.grad[:, :, :256].abs().sum().item(), 0)
        self.assertGreater(mel.grad[:, :, 256:].abs().sum().item(), 0)
