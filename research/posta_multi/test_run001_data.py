"""No external dataset: Run 001 text sampling and provenance tests."""
import unittest

from run001_data import (KINDS, POLICY, approval_issues,
                         balanced_validation_windows, make_windows, select_records)


class FakeCorpus:
    def __init__(self, groups):
        self.groups = groups

    def rows(self, kind, split):
        yield from self.groups.get((kind, split), [])


class FakeTokenized:
    def __init__(self, ids):
        self.ids = ids


class FakeTokenizer:
    def encode(self, text, add_special_tokens=True):
        if add_special_tokens:
            raise AssertionError('raw tokenization must exclude automatic CLS/SEP')
        return FakeTokenized([10 + ord(letter) % 15 for letter in text])


class Run001DataTests(unittest.TestCase):
    def test_all_supported_modalities_and_positive_documents(self):
        self.assertEqual(len(KINDS), 7)
        corpus = FakeCorpus({
            ('retrieval', 'train'): [{
                'text': 'a question', 'positive': 'an answer',
                'language': 'ru',
            }],
            ('ocr_text', 'train'): [{
                'text': 'image caption', 'ocr': 'visible letters',
                'language': 'en',
            }],
            ('video_text', 'train'): [{
                'text': 'clip caption', 'language': 'es',
            }],
        })
        selected, stats = select_records(corpus, split='train', max_per_group=8)
        self.assertEqual(len(selected), 5)
        self.assertEqual(stats['retrieval:ru']['selected'], 2)
        self.assertEqual(stats['ocr_text:en']['selected'], 2)
        self.assertEqual(stats['video_text:es']['selected'], 1)
        windows, digest, lengths = make_windows(selected, FakeTokenizer(), length=8)
        self.assertGreater(len(windows), 0)
        self.assertEqual(len(digest), 64)
        self.assertTrue(all(w[0] == 1 and w[-1] == 2 for w in windows))
        self.assertTrue(POLICY.startswith('posta-run001'))

    def test_stable_hash_independent_of_row_order(self):
        original = [{'text': f'text {i} for pretrain', 'language': 'ru'}
                    for i in range(40)]
        a = FakeCorpus({('text', 'train'): original})
        b = FakeCorpus({('text', 'train'): list(reversed(original))})
        one, _ = select_records(a, split='train', max_per_group=10)
        two, _ = select_records(b, split='train', max_per_group=10)
        self.assertEqual(one, two)
        self.assertEqual(make_windows(one, FakeTokenizer(), 12)[:2],
                         make_windows(two, FakeTokenizer(), 12)[:2])

    def test_rejects_test_split_and_missing_approval(self):
        corpus = FakeCorpus({('text', 'test'): [{'text': 'secret'}]})
        with self.assertRaisesRegex(ValueError, 'train or validation'):
            select_records(corpus, split='test', max_per_group=2)
        self.assertTrue(approval_issues({'status': 'screened'}))
        self.assertEqual(approval_issues({
            'status': 'approved_for_training',
            'privacy_review': 'passed',
            'source_rights_review': 'passed',
        }), [])


    def test_stratified_validation_is_stable_and_balanced(self):
        source = {
            ('text', 'validation'): [
                {'text': f'russian valid text {i} words', 'language': 'ru'}
                for i in range(10)] +
                [{'text': 'english valid text words', 'language': 'en'}],
            ('ocr_text', 'validation'): [
                {'text': 'letters on a picture', 'language': 'en'}],
        }
        records, _ = select_records(FakeCorpus(source), split='validation',
                                    max_per_group=20)
        first = balanced_validation_windows(records, FakeTokenizer(),
                                            length=12, per_group=2)
        windows, labels, fingerprint, counts = first
        self.assertEqual(len(windows), len(labels))
        self.assertEqual(len(fingerprint), 64)
        self.assertEqual(set(counts), {'text:ru', 'text:en', 'ocr_text:en'})
        self.assertTrue(all(1 <= n <= 2 for n in counts.values()))
        reversed_records, _ = select_records(FakeCorpus({
            key: list(reversed(rows)) for key, rows in source.items()
        }), split='validation', max_per_group=20)
        self.assertEqual(first, balanced_validation_windows(
            reversed_records, FakeTokenizer(), length=12, per_group=2))
        with self.assertRaises(ValueError):
            balanced_validation_windows(records, FakeTokenizer(), 12, per_group=0)

    def test_invalid_or_empty_windows(self):
        corpus = FakeCorpus({})
        with self.assertRaises(ValueError):
            select_records(corpus, split='train', max_per_group=2)
        with self.assertRaises(ValueError):
            make_windows([], FakeTokenizer(), length=2)


if __name__ == '__main__':
    unittest.main()
