import ctypes.util
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location('shortlist', Path(__file__).with_name('classification_shortlist.py'))
S = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(S)


class TopicSufficiencyTests(unittest.TestCase):
    def test_topic_known_does_not_require_complete_details(self):
        row = S.normalize_description({'topic_identifiable': True, 'details_complete': False,
                                       'description': 'Компания экономит на налогах', 'topic_guess': 'Налогообложение',
                                       'missing_details': 'Неизвестен точный механизм'})
        self.assertTrue(row['ready_for_label_selection'])
        self.assertFalse(row['details_complete'])

    def test_legacy_refusal_is_not_rewritten_as_permission(self):
        row = S.normalize_description({'sufficient': False, 'description': 'Есть текст о налогах', 'topic_guess': 'Налоги'})
        self.assertIsNone(row['topic_identifiable'])
        self.assertTrue(row['retrieval_text_available'])
        self.assertFalse(row['ready_for_label_selection'])
        self.assertTrue(row['legacy_ambiguous_refusal'])

    def test_explicit_unknown_topic_still_blocks_labels(self):
        row = S.normalize_description({'topic_identifiable': False, 'details_complete': False,
                                       'description': 'Нечитаемая обложка', 'topic_guess': ''})
        self.assertFalse(row['ready_for_label_selection'])

    def test_invalid_flag_and_unrelated_label_fields(self):
        with self.assertRaises(ValueError):
            S.normalize_description({'topic_identifiable': 'yes', 'details_complete': False,
                                     'description': 'x', 'topic_guess': 'y'})
        row = S.normalize_description({'sufficient': True, 'description': 'x', 'topic_guess': 'y',
                                       'required': ['leaked'], 'tags': ['old']})
        self.assertNotIn('required', row)
        self.assertNotIn('tags', row)


@unittest.skipUnless(ctypes.util.find_library('stemmer'), 'libstemmer needed')
class IndexTests(unittest.TestCase):
    def test_exact_alias_survives_bad_semantic_order(self):
        topics = [{'key': 'career', 'title': 'Карьера', 'about': 'Найм', 'aliases': ['подбор персонала']},
                  {'key': 'weather', 'title': 'Погода', 'about': 'Осадки'},
                  {'key': 'sport', 'title': 'Спорт', 'about': 'Матчи'}]
        index = S.CandidateIndex(topics, [[0, 1], [1, 0], [.9, .1]])
        try:
            rows = index.rank('Обсуждаются вопросы', 'Подбор персонала', [1, 0], [1, 0])
            self.assertEqual(rows[0]['key'], 'career')
            self.assertTrue(rows[0]['exact_alias'])
            self.assertEqual(len({r['key'] for r in rows}), 3)
        finally:
            index.stem.close()

    def test_negated_alias_not_reserved(self):
        index = S.CandidateIndex([{'key': 'code', 'title': 'Программирование', 'about': 'Код'},
                                  {'key': 'design', 'title': 'Дизайн', 'about': 'Рисунок'}], [[0, 1], [1, 0]])
        try:
            rows = index.rank('Создание рисунков', 'Без программирования', [1, 0], [1, 0])
            code = next(r for r in rows if r['key'] == 'code')
            self.assertIsNone(code['exact_alias'])
        finally:
            index.stem.close()

    def test_important_short_terms_preserved_and_boundaries_not_indexed(self):
        stem = S.Stemmer()
        try:
            self.assertIn('ии', stem.tokens('ИИ-агенты'))
        finally:
            stem.close()
        doc = S.embedding_document({'title': 'Карьера', 'about': 'Найм', 'aliases': ['подбор персонала'],
                                    'hint': 'Не программирование'})
        self.assertIn('подбор персонала', doc)
        self.assertNotIn('программирование', doc)


class OfflineTests(unittest.TestCase):
    def test_cache_cannot_silently_reuse_other_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'cache.json'
            S.write(path, {'input_sha256': S.digest(['first']), 'vectors': [[1, 0]]})
            with self.assertRaisesRegex(ValueError, 'other inputs'):
                S.local_embeddings(['second'], path)
            vectors, calls = S.local_embeddings(['first'], path)
            self.assertEqual(vectors, [[1, 0]])
            self.assertEqual(calls, 0)

    def test_multitopic_and_missing_case_are_not_hidden_by_denominator(self):
        ranked = {'records': {'a': {'candidates': [{'key': 'x'}]}, 'b': {'candidates': [{'key': 'y'}]}}}
        refs = {'cases': {'a': {'status': 'provisional', 'required': ['x', 'z']},
                           'b': {'status': 'provisional', 'required': ['y']},
                           'c': {'status': 'provisional', 'required': ['z']},
                           'd': {'status': 'disputed', 'required': ['x']}}}
        old = {'retrieval_coverage': {'a': {'all_expected_present': False}}}
        summary = S.evaluate(ranked, refs, old)
        self.assertEqual(summary['reference_cases'], 3)
        self.assertEqual(summary['descriptions_available'], 2)
        self.assertEqual(summary['any_at_5'], 2)
        self.assertEqual(summary['all_at_5'], 1)
        self.assertEqual(summary['same_previous_subset']['cases'], 1)
        self.assertEqual(summary['same_previous_subset']['after_all_at_5'], 0)


if __name__ == '__main__':
    unittest.main()
