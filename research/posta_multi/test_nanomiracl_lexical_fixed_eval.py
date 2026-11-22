import unittest
from nanomiracl_benchmark import evaluate_language, summarize
from nanomiracl_lexical_fixed_eval import evaluate, paired_intervals


class FrozenLexicalTests(unittest.TestCase):
    def test_reproduces_word_baseline_before_comparing(self):
        group = {'queries': {'q': 'красный автомобиль', 'r': 'вкусный суп'},
                 'corpus': {'a': 'красный автомобиль', 'b': 'вкусный суп'},
                 'qrels': {'q': ['a'], 'r': ['b']}}
        baseline = summarize(evaluate_language(group['queries'], group['corpus'], group['qrels']))
        report = evaluate({'ru': group, 'en': group}, {'languages': {'ru': baseline, 'en': baseline}})
        self.assertEqual(report['ru']['word_bm25']['hit_5'], 1.0)
        with self.assertRaisesRegex(ValueError, 'does not reproduce'):
            evaluate({'ru': group, 'en': group}, {'languages': {
                'ru': {**baseline, 'mrr': 0.0}, 'en': baseline}})

    def test_paired_bootstrap_keeps_aligned_queries(self):
        original = [{'hit_5': 0, 'mrr': .1}, {'hit_5': 0, 'mrr': .1}]
        improved = [{'hit_5': 1, 'mrr': .5}, {'hit_5': 1, 'mrr': .5}]
        result = paired_intervals(original, improved, draws=100)
        self.assertEqual(result['hit_5']['bootstrap_95_low'], 1.0)
        self.assertEqual(result['mrr']['delta'], 0.4)


if __name__ == '__main__':
    unittest.main()
