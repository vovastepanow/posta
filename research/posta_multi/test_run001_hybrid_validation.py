import unittest
from run001_hybrid_validation import language_metrics


class CorpusFixture:
    def rows(self, kind, split):
        assert (kind, split) == ('retrieval', 'validation')
        yield {'text': 'one', 'language': 'ru'}
        yield {'text': 'two', 'language': 'zh'}
        yield {'text': 'ambiguous', 'language': 'ru'}
        yield {'text': 'ambiguous', 'language': 'en'}


class HybridLanguageTests(unittest.TestCase):
    def test_reports_only_heldout_queries_by_language(self):
        pairs = [('one', 'p', 'n'), ('two', 'p', 'n'), ('ambiguous', 'p', 'n')]
        row = {'lexical': [2, 1], 'semantic': [1, 2], 'positive': 0, 'negative': 1}
        result = language_metrics(CorpusFixture(), pairs, [row]*3, [row]*3, 1, 0.0)
        self.assertEqual(set(result), {'zh', 'mixed_or_unknown'})
        self.assertEqual(result['zh']['word_bm25']['samples'], 1)
        with self.assertRaisesRegex(ValueError, 'aligned'):
            language_metrics(CorpusFixture(), pairs, [row], [row]*3, 1, 0.0)


if __name__ == '__main__':
    unittest.main()
