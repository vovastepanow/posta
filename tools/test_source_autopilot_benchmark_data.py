import unittest
import json
import tempfile
from pathlib import Path
from source_autopilot_benchmark_data import blind, merge


class BenchmarkDataTest(unittest.TestCase):
    def setUp(self):
        self.data = {'samples': [
            {'profile': 'A', 'sample_id': 'x', 'text': 'post', 'posted_at': 1, 'media_kinds': [], 'annotation': None, 'fixture': 'hidden'},
            {'profile': 'B', 'sample_id': 'x', 'text': 'post', 'posted_at': 1, 'media_kinds': [], 'annotation': None},
        ]}

    def test_blind_and_separate_results(self):
        result = blind(self.data)
        self.assertNotIn('fixture', result['samples'][0])
        self.assertNotIn('annotation', result['samples'][0])
        label = [{'profile': 'A', 'sample_id': 'x', 'annotation': {'relevant': True}}]
        baseline = [{'profile': 'B', 'sample_id': 'x', 'decision': False}]
        auto = [{'profile': 'A', 'sample_id': 'x', 'decision': True}]
        combined = merge(self.data, label, baseline, auto)
        self.assertTrue(combined['samples'][0]['annotation']['relevant'])
        self.assertTrue(combined['samples'][0]['decisions']['auto'])
        self.assertFalse(combined['samples'][1]['decisions']['baseline'])
        self.assertIsNone(self.data['samples'][0]['annotation'])

    def test_rejects_unknown_duplicates_and_invalid_types(self):
        unknown = {'profile': 'C', 'sample_id': 'x', 'decision': True}
        valid = {'profile': 'A', 'sample_id': 'x', 'decision': True}
        bad = {'profile': 'A', 'sample_id': 'x', 'decision': 1}
        for rows in ([unknown], [valid, valid], [bad]):
            with self.assertRaises(ValueError):
                merge(self.data, [], rows, [])

    def test_blind_media_excludes_credentials_and_query_strings(self):
        data = {'samples': [{'profile': 'A', 'sample_id': 'x', 'text': 'post', 'posted_at': 1,
                            'media_kinds': ['image'], 'fixture': 'source_preview_rss.json', 'fixture_index': 0}]}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'source_preview_rss.json').write_text(json.dumps({'discovered': {'posts': [{'text': 'post', 'posted_at': 1, 'media': [
                {'kind': 'image', 'src': 'https://example.com/image.jpg', 'poster': 'https://example.com/a?token=secret',
                 'page': 'https://user:password@example.com/page'}]}]}}))
            media = blind(data, root)['samples'][0]['media'][0]
            self.assertEqual(media, {'kind': 'image', 'src': 'https://example.com/image.jpg'})


if __name__ == '__main__':
    unittest.main()
