import tempfile
import unittest
from pathlib import Path
from join_mmarco import read_ids, read_selected


class JoinTests(unittest.TestCase):
    def test_bounded_triples_and_lookup(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            triples = root / 'triples.tsv'
            triples.write_text('1\t2\t3\n4\t5\t6\n', encoding='utf-8')
            self.assertEqual(read_ids(triples, 1), [(0, '1', '2', '3')])
            text = root / 'texts.tsv'
            text.write_text('1\tпервый\n2\tвторой\n3\tтретий\n', encoding='utf-8')
            self.assertEqual(read_selected(text, {'1','3'}), {'1':'первый','3':'третий'})

    def test_reject_malformed_triples(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'bad'
            path.write_text('1\t2\n', encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'invalid triple'):
                read_ids(path, 1)


if __name__ == '__main__':
    unittest.main()
