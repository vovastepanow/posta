import unittest
from run001_lexical_validation import CharacterBM25, character_trigrams


class CharacterBM25Tests(unittest.TestCase):
    def test_inflected_russian_shares_subwords(self):
        index = CharacterBM25(['обзор автомобиля', 'новости музыки'])
        scores = index.scores('автомобиль')
        self.assertGreater(scores[0], scores[1])

    def test_empty_and_unicode_inputs(self):
        self.assertEqual(character_trigrams(''), [])
        self.assertEqual(character_trigrams('ЁЖ'), character_trigrams('ёж'))
        self.assertEqual(CharacterBM25(['', '']).scores('слово'), [0.0, 0.0])
        with self.assertRaises(ValueError):
            CharacterBM25([])


if __name__ == '__main__':
    unittest.main()
