import unittest
from lexical_fusion_index import LexicalFusionIndex


class LexicalFusionIndexTests(unittest.TestCase):
    def test_post_text_drives_search_and_empty_queries_return_no_posts(self):
        index = LexicalFusionIndex({'cars': 'новости автомобиля',
                                    'food': 'кулинарный рецепт супа'})
        self.assertEqual(index.search('автомобиль')[0]['post_id'], 'cars')
        self.assertEqual(index.search(''), [])
        self.assertEqual(index.search('несовпадающийнаборсимволов'), [])

    def test_top_k_and_ties_are_deterministic(self):
        index = LexicalFusionIndex({str(i).zfill(3): 'космос звёзды' for i in range(105)})
        result = index.search('космос')
        self.assertEqual(len(result), 100)
        self.assertEqual(result[0]['post_id'], '000')
        self.assertEqual(result[-1]['post_id'], '099')
        with self.assertRaises(ValueError):
            index.search('космос', top_k=True)
        with self.assertRaises(ValueError):
            LexicalFusionIndex({})

    def test_source_metadata_cannot_be_used_as_post_text(self):
        with self.assertRaises(ValueError):
            LexicalFusionIndex({'post': {'source_title': 'автомобили',
                                        'text': 'новости политики'}})
        index = LexicalFusionIndex({'post': 'новости политики'})
        self.assertEqual(index.search('автомобили'), [])


if __name__ == '__main__':
    unittest.main()
