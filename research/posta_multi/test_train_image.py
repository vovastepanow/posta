import unittest
from train_image import load_rows


class FakeCorpus:
    def rows(self, kind, split):
        assert kind == 'image_text' and split == 'train'
        for text, sha in [('same', 'a'), ('same', 'b'), ('different', 'a'), ('last', 'c')]:
            yield {'text': text, 'dataset_id': 'dataset', 'dataset_revision': 'rev', 'media': {'sha256': sha}}


class ImageTrainingTests(unittest.TestCase):
    def test_duplicate_captions_and_media_not_selected(self):
        rows, digest = load_rows(FakeCorpus(), 2)
        self.assertEqual([row['text'] for row in rows], ['same', 'last'])
        self.assertEqual(len(digest), 64)
        with self.assertRaises(ValueError):
            load_rows(FakeCorpus(), 3)
