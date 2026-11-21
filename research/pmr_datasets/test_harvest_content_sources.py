"""Offline tests for SHA-logged, never-approved news/forum ingestion."""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from urllib.parse import parse_qs, urlparse
from unittest.mock import patch

from harvest_content_sources import (FORMAT, SOURCES, harvest,
                                     prepare, record_for, sampling_offset)


def example_record(number, *, license_value='CC BY 4.0', url=None):
    return {
        'row_idx': number,
        'row': {
            'id': str(number),
            'text': 'A community post about technology and the news. ' * 4 + str(number),
            'source': 'news-360info',
            'created': '2022-11-01',
            'metadata': {
                'url': url or f'https://example.test/article/{number}',
                'license': license_value,
                'author': 'Test author',
            },
        },
    }


def fake_get_json(url):
    parts = urlparse(url)
    if parts.path.endswith('/rows'):
        query = parse_qs(parts.query)
        count = int(query['length'][0])
        offset = int(query['offset'][0])
        assert query['split'] == ['train']
        assert query['config'] == ['default']
        return {'rows': [example_record(offset + i) for i in range(count)]}
    for source in SOURCES.values():
        if parts.path.endswith('/' + source['repo']):
            return {'sha': source['revision']}
    raise AssertionError('unexpected URL ' + url)


class HarvestSourceTests(unittest.TestCase):
    def test_real_verified_source_revisions_have_distinct_caps(self):
        self.assertEqual(set(SOURCES), {'common-pile-news-filtered',
                                        'common-pile-stackexchange'})
        self.assertNotEqual(SOURCES['common-pile-news-filtered']['revision'],
                            SOURCES['common-pile-stackexchange']['revision'])
        for id_, config in SOURCES.items():
            positions = [sampling_offset(id_, i, config['offset_ceiling'], 5)
                         for i in range(20)]
            self.assertEqual(len(positions), len(set(positions)))
            self.assertTrue(all(0 <= i <= config['offset_ceiling'] - 5
                                for i in positions))
            self.assertEqual(positions[0], sampling_offset(
                id_, 0, config['offset_ceiling'], 5))

    def test_row_preserves_license_attribution_without_inventing_labels(self):
        src = 'common-pile-stackexchange'
        row = record_for(src, example_record(3), max_chars=600)
        self.assertEqual(row['dataset_id'], src)
        self.assertEqual(row['source_license_claim'], 'CC BY 4.0')
        self.assertEqual(row['attribution_authors'], ['Test author'])
        self.assertEqual(row['content_category'], 'community_discussion')
        self.assertEqual(row['training_approved'], False)
        self.assertEqual(row['source_rights_review'], 'pending')
        self.assertEqual(row['kind'], 'text')
        self.assertNotIn('positive', row)
        self.assertNotIn('negatives', row)
        self.assertEqual(row['group_id'], 'https://example.test/article/3')
        with self.assertRaises(ValueError):
            record_for(src, example_record(3, url='http://example.test/bad'),
                       max_chars=500)
        with self.assertRaises(ValueError):
            record_for(src, example_record(3, license_value=''),
                       max_chars=500)

    @patch('harvest_content_sources.get_json', side_effect=fake_get_json)
    def test_two_sources_generate_real_quarantined_files_and_repeatable_checksums(self, _):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            first = prepare(base/'first', sources=list(SOURCES),
                            count=6, batch_size=3, max_chars=500)
            second = prepare(base/'second', sources=list(SOURCES),
                             count=6, batch_size=3, max_chars=500)
            self.assertEqual(first['format'], FORMAT)
            self.assertFalse(first['training_approved'])
            self.assertEqual(len(first['sources']), 2)
            self.assertEqual([f['sha256'] for f in first['files']],
                             [f['sha256'] for f in second['files']])
            for entry in first['files']:
                path = base/'first'/entry['path']
                self.assertEqual(entry['sha256'],
                                 hashlib.sha256(path.read_bytes()).hexdigest())
                rows = [json.loads(s) for s in path.read_text().splitlines()]
                self.assertEqual(len(rows), entry['rows'])
                self.assertTrue(all(row['training_approved'] is False for row in rows))
                self.assertTrue(all(row['source_split'] == 'train' for row in rows))
            self.assertTrue(all('NOT pinned' not in s for s in first['limitations']))
            self.assertTrue(any('NOT pinned' in s for s in first['limitations']))
            with self.assertRaises(FileExistsError):
                prepare(base/'first', sources=list(SOURCES),
                        count=6, batch_size=3, max_chars=500)

    @patch('harvest_content_sources.get_json')
    def test_revision_change_blocks_and_removes_candidate(self, getter):
        real = SOURCES['common-pile-news-filtered']['revision']
        calls = 0
        def head_moves(url):
            nonlocal calls
            if urlparse(url).path.endswith('/rows'):
                return fake_get_json(url)
            calls += 1
            return {'sha': real if calls == 1 else 'f' * 40}
        getter.side_effect = head_moves
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / 'bad'
            with self.assertRaisesRegex(ValueError, 'upstream HEAD moved'):
                prepare(target, sources=['common-pile-news-filtered'],
                        count=4, batch_size=2, max_chars=500)
            self.assertFalse(target.exists())

    @patch('harvest_content_sources.get_json')
    def test_missing_license_fails_closed(self, getter):
        def no_license(url):
            result = fake_get_json(url)
            if urlparse(url).path.endswith('/rows'):
                for entry in result['rows']:
                    entry['row']['metadata']['license'] = ''
            return result
        getter.side_effect = no_license
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'candidate'
            with self.assertRaisesRegex(ValueError, 'insufficient candidate'):
                prepare(path, sources=['common-pile-news-filtered'],
                        count=4, batch_size=2, max_chars=500)
            self.assertFalse(path.exists())

    def test_invalid_input_rejected_without_network(self):
        with tempfile.TemporaryDirectory() as tmp:
            for kwargs in (
                {'sources': [], 'count': 4, 'batch_size': 2, 'max_chars': 600},
                {'sources': ['unknown'], 'count': 4, 'batch_size': 2, 'max_chars': 600},
                {'sources': ['common-pile-news-filtered'], 'count': 1, 'batch_size': 2, 'max_chars': 600},
                {'sources': ['common-pile-news-filtered'], 'count': 4, 'batch_size': 0, 'max_chars': 600},
            ):
                with self.subTest(kwargs=kwargs):
                    with self.assertRaises(ValueError):
                        prepare(Path(tmp)/'bad', **kwargs)


if __name__ == '__main__':
    unittest.main()
