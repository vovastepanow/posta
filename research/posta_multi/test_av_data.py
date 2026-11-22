import unittest

from av_data import select_media_pairs


def row(identifier, group, text, media):
    return {'record_id': identifier, 'dataset_id': 'sample', 'group_id': group,
            'language': 'en', 'text': text, 'duration_seconds': 3,
            'media': {'sha256': media * 64}}


class FakeCorpus:
    def __init__(self, train, validation):
        self.data = {'train': train, 'validation': validation}

    def rows(self, kind, split):
        return iter(self.data[split])


class SelectionTests(unittest.TestCase):
    def test_excludes_exact_group_media_and_text_overlap(self):
        corpus = FakeCorpus([
            row('train', 'g1', 'training caption', 'a'),
            row('train2', 'g2', 'other caption', 'b')], [
            row('bad-group', 'g1', 'different caption', 'c'),
            row('bad-text', 'g3', 'training caption', 'd'),
            row('bad-media', 'g4', 'different text', 'a'),
            row('good1', 'g5', 'first held out', 'e'),
            row('good2', 'g6', 'second held out', 'f')])
        selected, digest = select_media_pairs(corpus, 'video_text', 'validation', 2)
        self.assertEqual({x['record_id'] for x in selected}, {'good1', 'good2'})
        self.assertEqual(len(digest), 64)
        self.assertEqual(digest, select_media_pairs(corpus, 'video_text', 'validation', 2)[1])

    def test_speech_allows_multiple_utterances_from_held_out_group(self):
        corpus = FakeCorpus([row('t', 'train', 'training', 'a')], [
            row('v1', 'heldout', 'first phrase', 'b'),
            row('v2', 'heldout', 'second phrase', 'c')])
        selected, _ = select_media_pairs(corpus, 'speech_text', 'validation', 2)
        self.assertEqual(len(selected), 2)


if __name__ == '__main__':
    unittest.main()
