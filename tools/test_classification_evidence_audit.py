import unittest
from classification_evidence_audit import audit, evidence_status
import json


class EvidenceAuditTests(unittest.TestCase):
    def test_metadata_and_actual_subject_are_distinct(self):
        self.assertEqual(evidence_status('Аудитория #продажи', 'продажи'), 'metadata_only')
        self.assertEqual(evidence_status('Ссылка https://a.test/продажи', 'продажи'), 'metadata_only')
        self.assertEqual(evidence_status('Продажи: этапы сделки. #продажи', 'Продажи: этапы сделки'), 'literal_match_semantics_unchecked')
        self.assertEqual(evidence_status('Иски к рекламной группе', 'рекламной группе'), 'literal_match_semantics_unchecked')
        self.assertEqual(evidence_status('Налоги', 'Отчётность'), 'not_in_source')

    def test_no_recovery_or_hidden_denominator(self):
        records = [{'id': 'a', 'full_text': 'Текст'}, {'id': 'b', 'full_text': 'Другой текст'}]
        request = {'input': [{'content': [{'text': 'Публикации:\n' + json.dumps(records)}]}]}
        result = audit(request, [
            {'number': 1, 'text': 'broken', 'answer': {'items': []}},
            {'number': 2, 'text': json.dumps({'items': [{'id': 'a', 'topics': [], 'evidence': []}]})},
            {'number': 3},
        ])
        self.assertEqual(result['unique_records'], 2)
        self.assertEqual(result['trials'][0]['status'], 'invalid_envelope')
        self.assertEqual(result['trials'][0]['records_unassessed'], ['a', 'b'])
        self.assertEqual(result['trials'][1]['records_unassessed'], ['b'])
        self.assertEqual(result['trials'][2]['status'], 'no_response_text')

    def test_unknown_evidence_source_cannot_borrow_text(self):
        records = [{'id': 'a', 'full_text': 'Текст', 'topic_guess': 'Выдумка'}]
        request = {'input': [{'content': [{'text': 'Публикации:\n' + json.dumps(records)}]}]}
        answer = {'items': [{'id': 'a', 'topics': ['x'], 'evidence': [{'tag': 'x', 'source': 'topic_guess', 'quote': 'Выдумка'}]}]}
        row = audit(request, [{'number': 1, 'text': json.dumps(answer)}])['trials'][0]['rows'][0]
        self.assertEqual(row['evidence_status'], 'missing_source_or_quote')
        self.assertFalse(row['automatic_application_allowed'])


if __name__ == '__main__':
    unittest.main()
