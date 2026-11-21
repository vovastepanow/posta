"""Synthetic retrieval label-curation and train/eval isolation tests."""
import unittest

from retrieval_data_quality import CURATION_POLICY, curate_retrieval_pairs


class StubCorpus:
    def __init__(self, rows, validation=None, test=None):
        self.parts = {'train': rows, 'validation': validation or [], 'test': test or []}

    def rows(self, kind, split):
        assert kind == 'retrieval'
        yield from self.parts[split]


class CurationTests(unittest.TestCase):
    def test_contradictory_negative_across_same_query_rows_is_rejected(self):
        rows = [
            {'text': 'Ищу  МАШИНЫ', 'positive': 'Audi', 'negatives': ['bmw', ' Volvo ']},
            {'text': 'ищу машины', 'positive': 'BMW', 'negatives': ['Audi', 'Tesla']},
            {'text': 'Коты', 'positive': 'Кошки', 'negatives': ['собаки']},
        ]
        pairs, digest, stats = curate_retrieval_pairs(
            StubCorpus(rows), 2, avoid_heldout=False)
        self.assertEqual(len(digest), 64)
        self.assertEqual(len(pairs), 2)
        self.assertEqual(stats['negative_matches_known_positive'], 2)
        self.assertEqual(stats['eligible_distinct_queries'], 2)
        for q, p, n in pairs:
            if q.casefold().startswith(('ищу', 'Ищу'.casefold())):
                self.assertNotIn(n.strip().casefold(), ('audi', 'bmw'))

    def test_deterministic_across_row_and_negative_orders(self):
        rows = [
            {'text': f'Запрос {i}', 'positive': f'ответ {i}',
             'negatives': [f'минус {i}', f'не то {i}']}
            for i in range(18)
        ]
        base = curate_retrieval_pairs(StubCorpus(rows), 7, avoid_heldout=False)
        reordered = [dict(r, negatives=list(reversed(r['negatives'])))
                     for r in reversed(rows)]
        alt = curate_retrieval_pairs(StubCorpus(reordered), 7, avoid_heldout=False)
        self.assertEqual(base[:2], alt[:2])
        self.assertEqual(base[2]['eligible_distinct_queries'], 18)
        self.assertEqual(CURATION_POLICY, 'posta-retrieval-curated-v1')

    def test_excludes_train_examples_seen_in_evaluation(self):
        rows = [
            {'text': 'one', 'positive': 'doc one', 'negatives': ['no one']},
            {'text': 'two', 'positive': 'doc two', 'negatives': ['no two']},
            {'text': 'three', 'positive': 'doc three', 'negatives': ['no three']},
        ]
        val = [{'text': ' ONE ', 'positive': 'val', 'negatives': []}]
        test = [{'text': 'X', 'positive': 'DOC TWO', 'negatives': []}]
        pairs, _, stats = curate_retrieval_pairs(
            StubCorpus(rows, val, test), 1)
        self.assertEqual(pairs, [('three', 'doc three', 'no three')])
        self.assertEqual(stats['rows_excluded_heldout_text'], 2)
        with self.assertRaisesRegex(ValueError, 'not enough curated queries'):
            curate_retrieval_pairs(StubCorpus(rows, val, test), 2)

    def test_rejects_negative_equal_to_query_and_unusable_rows(self):
        rows = [
            {'text': 'abc', 'positive': 'positive', 'negatives': ['ＡＢＣ', 'valid']},
            {'text': 'def', 'positive': 'answer', 'negatives': [' answer ']},
        ]
        pairs, _, stats = curate_retrieval_pairs(StubCorpus(rows), 1, avoid_heldout=False)
        self.assertEqual(pairs, [('abc', 'positive', 'valid')])
        self.assertEqual(stats['negative_matches_query'], 1)
        self.assertEqual(stats['negative_matches_known_positive'], 1)
        self.assertEqual(stats['rows_without_usable_negative'], 1)

    def test_validation_split_requires_explicit_no_heldout_exclusion(self):
        corpus = StubCorpus([{'text':'a','positive':'b','negatives':['c']}])
        with self.assertRaisesRegex(ValueError, 'only supported for training'):
            curate_retrieval_pairs(corpus, 1, split='validation', avoid_heldout=True)
        with self.assertRaises(ValueError):
            curate_retrieval_pairs(corpus, 0)
        with self.assertRaises(ValueError):
            curate_retrieval_pairs(corpus, True)
