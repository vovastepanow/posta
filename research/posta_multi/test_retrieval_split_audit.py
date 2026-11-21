"""Small, synthetic, offline retrieval split isolation checks."""
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace

from retrieval_split_audit import audit_retrieval_splits, fingerprint, normalize_text


class Corpus:
    def __init__(self):
        self.manifest_hash = 'known-stub-manifest'
        self.data = {
            'train': [
                {'text':' What   is POSTA? ', 'positive':'An answer to Posta',
                 'negatives':['not relevant', 'NOT   RELEVANT', 'An answer to Posta'],
                 'dataset_id':'A', 'language':'ru'},
                {'text':'new', 'positive':'other', 'negatives':[], 'dataset_id':'A', 'language':'en'},
            ],
            'validation': [
                {'text':'Completely different question',
                 'positive':'An answer to Posta', 'negatives':['unknown'],
                 'dataset_id':'B', 'language':'en'},
            ],
            'test': [
                {'text':'what is posta?', 'positive':' AN ANSWER to posta ',
                 'negatives':['other'], 'dataset_id':'A', 'language':'ru'},
                {'text':'other query','positive':'not relevant', 'negatives':[],
                 'dataset_id':'C', 'language':'und'},
            ],
        }

    def rows(self, kind, split):
        assert kind == 'retrieval'
        yield from self.data[split]


class SplitAuditTests(unittest.TestCase):
    def test_normalization_unicode_whitespace_case(self):
        self.assertEqual(normalize_text('  A　Ｂ   c\n'), 'a b c')
        self.assertEqual(fingerprint('An  answer'), fingerprint('an answer'))
        self.assertIsNone(fingerprint('  '))

    def test_counts_and_cross_split_collisions(self):
        report = audit_retrieval_splits(Corpus())
        self.assertEqual(report['format'], 'posta-retrieval-split-audit-v1')
        self.assertEqual(report['manifest_sha256'], 'known-stub-manifest')
        self.assertEqual(report['splits']['train']['rows'], 2)
        self.assertEqual(report['splits']['train']['with_explicit_negatives'], 1)
        self.assertEqual(report['splits']['train']['without_explicit_negatives'], 1)
        self.assertEqual(report['splits']['train']['negative_equals_positive'], 1)
        self.assertEqual(report['splits']['train']['duplicate_negative_values'], 1)
        self.assertEqual(report['splits']['train']['languages'], {'en':1, 'ru':1})
        self.assertEqual(report['dataset_counts']['test'], {'A':1, 'C':1})
        crossing = report['exact_cross_split_overlap']['train_vs_test']
        self.assertEqual(crossing['exact_query_positive_pairs'], 1)
        self.assertEqual(crossing['exact_shared_query_texts'], 1)
        self.assertEqual(crossing['exact_shared_positive_texts'], 1)
        self.assertEqual(crossing['positive_in_other_negatives'], 1)
        self.assertEqual(crossing['negative_in_other_positives'], 2)
        self.assertEqual(report['exact_cross_split_overlap']['train_vs_validation']['exact_shared_positive_texts'], 1)

    def test_never_emits_raw_queries_or_documents(self):
        text = str(audit_retrieval_splits(Corpus()))
        for secret in ('What   is POSTA?', 'An answer to Posta', 'Completely different question'):
            self.assertNotIn(secret, text)

    def test_no_missing_positives_are_falsely_approved(self):
        corpus = Corpus()
        corpus.data['validation'].append(
            {'text':'q2', 'positive':'', 'negatives':['a'], 'dataset_id':'B', 'language':'en'})
        report = audit_retrieval_splits(corpus)
        self.assertEqual(report['splits']['validation']['invalid_or_missing_positives'], 1)


if __name__ == '__main__':
    unittest.main()
