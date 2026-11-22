import unittest
from video_caption_validation import select_validation_pairs


def video(group, brief, detailed, media):
    return {'language': 'en', 'dataset_id': 'v', 'group_id': group,
            'text': brief, 'detailed_caption': detailed, 'media_sha256': media * 64}


class Fixture:
    def __init__(self, rows):
        self.data = rows

    def rows(self, kind, split):
        yield from self.data.get((kind, split), [])


class CaptionValidationTests(unittest.TestCase):
    def test_only_isolated_validation_video_groups_and_stable_order(self):
        old = video('used', 'A previously seen brief video caption',
                    'A detailed description of an earlier training clip', 'a')
        first = video('new1', 'A caption about a green hillside',
                      'A detailed description of a quiet hillside scene', 'b')
        second = video('new2', 'A caption about a modern train',
                       'A detailed description of a train on a long track', 'c')
        duplicate = video('used', 'A different caption from the same group',
                          'A different detailed description of the same video', 'd')
        one = Fixture({('video_text', 'train'): [old],
                       ('video_text', 'validation'): [first, duplicate, second]})
        two = Fixture({('video_text', 'train'): [old],
                       ('video_text', 'validation'): [second, first, duplicate]})
        chosen, digest = select_validation_pairs(one, 2)
        self.assertEqual((chosen, digest), select_validation_pairs(two, 2))
        self.assertEqual(set(chosen), {(first['text'], first['detailed_caption']),
                                       (second['text'], second['detailed_caption'])})
        with self.assertRaisesRegex(ValueError, 'only 2'):
            select_validation_pairs(one, 3)


if __name__ == '__main__':
    unittest.main()
