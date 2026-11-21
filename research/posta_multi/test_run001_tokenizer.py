"""Offline tests for multilingual Run 001 tokenizer intake and manual fitting."""
import argparse
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from run001_tokenizer import (MAX_CHARS, balanced_texts, fingerprint, heldout_collisions,
                              inspect, languages_arg, train, validation_metrics)


class FakeCorpus:
    def __init__(self, groups, approved=False):
        self.groups = groups
        self.manifest_hash = 'a' * 64
        self.manifest = ({
            'status': 'approved_for_training',
            'source_rights_review': 'passed',
            'privacy_review': 'passed',
        } if approved else {'status': 'quarantined'})
        self.calls = []

    def rows(self, kind, split):
        self.calls.append((kind, split))
        yield from self.groups.get((kind, split), [])


def make_corpus(*, approved=False, overlap=False):
    records = {}
    for lang, prefix in (('ru', 'Русская публикация'), ('en', 'English publication')):
        records[('text', 'train')] = records.get(('text', 'train'), []) + [
            {'text': f'{prefix} about many interesting events number {i} and details',
             'language': lang} for i in range(32)]
        records[('ocr_text', 'train')] = records.get(('ocr_text', 'train'), []) + [
            {'text': f'{prefix} image {i} with visual characters',
             'ocr': f'{prefix} label {i} contains text',
             'language': lang} for i in range(10)]
        records[('text', 'validation')] = records.get(('text', 'validation'), []) + [
            {'text': f'{prefix} heldout news article number {i} with many words',
             'language': lang} for i in range(8)]
        records[('text', 'test')] = records.get(('text', 'test'), []) + [
            {'text': f'{prefix} test passage number {i} with many words',
             'language': lang} for i in range(4)]
    if overlap:
        # The heldout text matches the *selected* train input after NFKC+casefold.
        common = 'unique exact leakage string shared between inputs'
        records[('retrieval', 'train')] = [
            {'text': common, 'language': 'ru'}]
        records[('retrieval', 'test')] = [
            {'text': 'unrelated query', 'positive': '  UNIQUE EXACT LEAKAGE STRING   SHARED between inputs ', 'language': 'en'}]
    return FakeCorpus(records, approved=approved)


def args(dataset, output=None, audit_only=False):
    return argparse.Namespace(
        dataset=str(dataset), output=output, audit_only=audit_only,
        languages='ru,en', per_group=128, per_language=24,
        min_documents=16, validation_per_group=32,
        min_validation=4, vocab_size=640,
    )


class Run001TokenizerTests(unittest.TestCase):
    def test_language_parser_and_stable_modality_balancing(self):
        self.assertEqual(languages_arg('RU, en'), ('ru', 'en'))
        for bad in ('', 'ru,ru', 'ru,', 'ru,unknown123', 'en_uk'):
            with self.assertRaises(ValueError):
                languages_arg(bad)
        selected = [
            (('text', 'ru'), 'Repeated normalized text'),
            (('ocr_text', 'en'), '  REPEATED   NORMALIZED text '),
            (('ocr_text', 'ru'), 'Russian other content'),
            (('text', 'en'), 'English other content'),
        ]
        first, counts = balanced_texts(selected, ('ru', 'en'), 3)
        second, _ = balanced_texts(list(reversed(selected)), ('ru', 'en'), 3)
        self.assertEqual(first, second)
        self.assertEqual(sum(counts.values()), 3)
        self.assertEqual(len({fingerprint(t) for _, _, t in first}), 3)

    def test_quarantined_audit_is_non_training_and_reports_roles(self):
        fake = make_corpus()
        with patch('run001_tokenizer.Corpus', return_value=fake):
            report = train(args('/unneeded', audit_only=True))
        self.assertFalse(report['ready_for_manual_tokenizer_training'])
        self.assertTrue(report['approval_issues'])
        self.assertEqual(report['selected_train_counts'], {'ru': 24, 'en': 24})
        self.assertEqual(report['heldout']['test']['distinct_selected_train_overlaps'], 0)
        self.assertIn(('retrieval', 'test'), fake.calls)

    def test_holdout_retrieval_positive_cannot_become_tokenizer_train(self):
        fake = make_corpus(approved=True, overlap=True)
        with patch('run001_tokenizer.Corpus', return_value=fake):
            selected, val, report = inspect(
                fake, ('ru', 'en'), per_group=128, per_language=100,
                min_documents=10, validation_per_group=16, min_validation=2)
        self.assertTrue(val)
        self.assertIn('train_tokenizer_input_overlaps_heldout_text',
                      report['approval_issues'])
        self.assertEqual(report['heldout']['test']['distinct_selected_train_overlaps'], 1)
        with tempfile.TemporaryDirectory() as directory:
            with patch('run001_tokenizer.Corpus', return_value=fake):
                test_args = args('/unneeded', output=Path(directory) / 'tokenizer')
                test_args.per_language = 100
                with self.assertRaisesRegex(ValueError, 'overlaps_heldout'):
                    train(test_args)
                self.assertFalse(test_args.output.exists())

    def test_approved_fixture_produces_real_bpe_and_metrics(self):
        try:
            from tokenizers import Tokenizer
        except ImportError:
            self.skipTest('tokenizers dependency unavailable')
        fake = make_corpus(approved=True)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'tokenizer'
            with patch('run001_tokenizer.Corpus', return_value=fake):
                metadata = train(args('/unneeded', output=path))
            self.assertEqual(metadata['training_examples'], 48)
            self.assertEqual(metadata['selected_train_counts'], {'ru': 24, 'en': 24})
            self.assertEqual(metadata['model_vocabulary_budget'], 65536)
            self.assertTrue(metadata['ready_for_manual_tokenizer_training'])
            tokenizer = Tokenizer.from_file(str(path / 'tokenizer.json'))
            self.assertEqual([tokenizer.token_to_id(t)
                              for t in ('[PAD]', '[CLS]', '[SEP]', '[MASK]')],
                             [0, 1, 2, 3])
            for phrase in ('Яблоко зелёное 🌍', 'Greetings, κόσμε!',
                           '你好世界', 'مرحبا بالعالم'):
                encoded = tokenizer.encode(phrase)
                self.assertEqual((encoded.ids[0], encoded.ids[-1]), (1, 2))
                self.assertEqual(tokenizer.decode(encoded.ids), phrase)
            self.assertTrue(all(row['unicode_round_trip_passed'] for row in
                                metadata['validation_metrics'].values()))
            self.assertEqual(metadata['tokenizer_sha256'],
                             hashlib.sha256((path / 'tokenizer.json').read_bytes()).hexdigest())
            with patch('run001_tokenizer.Corpus', return_value=fake):
                with self.assertRaises(FileExistsError):
                    train(args('/unneeded', output=path))

    def test_no_language_and_no_approval_blocks_training(self):
        fake = make_corpus()
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'tokenizer'
            with patch('run001_tokenizer.Corpus', return_value=fake):
                with self.assertRaisesRegex(ValueError, 'tokenizer training blocked'):
                    train(args('/unneeded', output=output))
            self.assertFalse(output.exists())


if __name__ == '__main__':
    unittest.main()
