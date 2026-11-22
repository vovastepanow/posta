import unittest
from retrieval_data_quality import curate_video_caption_pairs


def video(group, text, detail, media):
    return {'language': 'en', 'dataset_id': 'video', 'group_id': group,
            'text': text, 'detailed_caption': detail, 'media_sha256': media * 64}


class Fixture:
    def __init__(self, rows):
        self.data = rows

    def rows(self, kind, split):
        yield from self.data.get((kind, split), [])


class VideoCaptionPairTests(unittest.TestCase):
    def test_deterministic_weak_pairs_exclude_heldout_text_and_video(self):
        valid = video('a', 'A brief description of a vehicle',
                      'A longer description of the same vehicle in motion', 'a')
        overlap = video('b', 'A separate detailed clip about music',
                        'A detailed account of the same musical performance', 'b')
        group_overlap = video('c', 'A brief caption about the kitchen',
                              'A detailed description of a person cooking food', 'c')
        heldout = {'text': '  A SEPARATE DETAILED CLIP ABOUT MUSIC  '}
        heldout_video = video('c', 'A totally different heldout video caption',
                              'Another heldout description for the same group', 'd')
        corpus = Fixture({('video_text', 'train'): [valid, overlap, group_overlap],
                          ('text', 'validation'): [heldout],
                          ('video_text', 'test'): [heldout_video]})
        pairs, digest, stats = curate_video_caption_pairs(corpus, 10)
        self.assertEqual(pairs, [(valid['text'], valid['detailed_caption'])])
        self.assertEqual(stats['heldout_text_or_video'], 2)
        self.assertEqual(len(digest), 64)
        self.assertEqual(curate_video_caption_pairs(corpus, 10)[1], digest)
        self.assertEqual(curate_video_caption_pairs(corpus, 0)[0], [])
        with self.assertRaises(ValueError):
            curate_video_caption_pairs(corpus, -1)

    def test_sampling_does_not_depend_on_input_order(self):
        first = video('a', 'A short description about a bridge',
                      'A longer description of a bridge over water', 'a')
        second = video('b', 'A brief description of a train',
                       'A longer description of the train on the track', 'b')
        one = Fixture({('video_text', 'train'): [first, second]})
        two = Fixture({('video_text', 'train'): [second, first]})
        self.assertEqual(curate_video_caption_pairs(one, 1)[:2],
                         curate_video_caption_pairs(two, 1)[:2])


if __name__ == '__main__':
    unittest.main()
