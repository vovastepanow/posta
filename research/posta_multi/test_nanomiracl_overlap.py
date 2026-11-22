import unittest
from nanomiracl_overlap import audit


class CorpusFixture:
    def rows(self, kind, split):
        assert split in ('train', 'validation')
        if kind == 'retrieval':
            yield {'text': '  A QUERY ', 'positive': 'answer',
                   'negatives': ['OTHER DOCUMENT', 'unrelated']}
        elif kind == 'text':
            yield {'text': 'other document'}
        elif kind == 'video_text':
            yield {'text': 'video', 'detailed_caption': 'A QUERY'}


class OverlapTests(unittest.TestCase):
    def test_normalized_overlaps_are_counted_without_leaking_text(self):
        groups = {'en': {'queries': {'q': 'a query'},
                         'corpus': {'d': 'Other document', 'x': 'not seen'}}}
        result = audit(groups, CorpusFixture())
        self.assertEqual(result['unique_benchmark_texts_matched'],
                         {'en:corpus': 1, 'en:queries': 1})
        self.assertEqual(result['matched_train_rows_by_kind_and_benchmark'],
                         {'retrieval:en:corpus': 1, 'retrieval:en:queries': 1,
                          'text:en:corpus': 1, 'video_text:en:queries': 1})
        self.assertNotIn('Other document', str(result))
        validation = audit(groups, CorpusFixture(), split='validation')
        self.assertIn('matched_validation_rows_by_kind_and_benchmark', validation)
        with self.assertRaises(ValueError):
            audit(groups, CorpusFixture(), split='test')


if __name__ == '__main__':
    unittest.main()
