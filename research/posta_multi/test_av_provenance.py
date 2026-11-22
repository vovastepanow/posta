import copy
import unittest

from av_provenance import validate_av_source


class AVProvenanceTests(unittest.TestCase):
    def test_requires_matching_quarantine_and_healthy_retrieval(self):
        state = {'format': 'posta-multi-235m-run001-retrieval-v1-best', 'step': 25,
                 'selection_policy': 'healthy-max-mrr-v2',
                 'settings': {'manifest_sha256': 'manifest', 'tokenizer_sha256': 'tokenizer',
                              'quarantined_research_only': True, 'private_local_only': True,
                              'production_promotion_allowed': False},
                 'metrics': {'pair_accuracy': .7, 'mean_positive_minus_negative': .02,
                             'query_interexample_cosine': .7, 'document_interexample_cosine': .7,
                             'mean_reciprocal_rank': .3}}
        self.assertIs(validate_av_source(state, 'manifest', 'tokenizer'), state['settings'])
        for field in ('manifest_sha256', 'tokenizer_sha256', 'private_local_only'):
            altered = copy.deepcopy(state)
            altered['settings'][field] = False
            with self.subTest(field=field), self.assertRaises(ValueError):
                validate_av_source(altered, 'manifest', 'tokenizer')
        collapsed = copy.deepcopy(state)
        collapsed['metrics']['document_interexample_cosine'] = .999
        with self.assertRaisesRegex(ValueError, 'health'):
            validate_av_source(collapsed, 'manifest', 'tokenizer')


if __name__ == '__main__':
    unittest.main()
