import unittest
from train_text import mixed_examples
from tokenizer import CLS, SEP, encode


class FakeCorpus:
    def rows(self, kind, split):
        assert split == 'train'
        for language in ('ru', 'en'):
            yield {'language': language, 'text': 'a' * 48}


class MixedSampleTests(unittest.TestCase):
    def test_long_text_is_split_without_losing_tokens(self):
        windows, digest = mixed_examples(FakeCorpus(), 1, 16, encode)
        self.assertEqual(len(digest), 64)
        self.assertEqual(len(windows), 8 * 4)
        self.assertTrue(all(w[0] == CLS and w[-1] == SEP and len(w) <= 16 for w in windows))
        self.assertEqual(sum(len(w)-2 for w in windows), 8*48)
