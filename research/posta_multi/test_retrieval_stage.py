import unittest
import torch
from model import PostaMulti
from synthetic_train import tiny_config
from retrieval_stage import load_pairs, load_pairs_sampled, pair_loss
from train_retrieval import text_parameters


class FakeCorpus:
    def rows(self, kind, split):
        assert kind == 'retrieval' and split == 'train'
        yield {'text': 'q', 'positive': 'p', 'negatives': []}
        yield {'text': 'q', 'positive': 'p', 'negatives': ['p', 'n']}


class FakeTokenized:
    def __init__(self, text):
        self.ids = [1, 4 + ord(text[0]) % 50, 2]


class FakeTokenizer:
    def enable_truncation(self, max_length):
        pass
    def encode(self, text):
        return FakeTokenized(text)


class RetrievalTests(unittest.TestCase):
    def test_source_negatives_and_gradient(self):
        pairs, sha = load_pairs(FakeCorpus(), 1)
        self.assertEqual(pairs, [('q', 'p', 'n')])
        self.assertEqual(len(sha), 64)
        model = PostaMulti(tiny_config(), dropout=0)
        loss = pair_loss(model, FakeTokenizer(), pairs[0], 'cpu')
        loss.backward()
        self.assertTrue(torch.isfinite(loss))
        self.assertGreater(model.text_projection.weight.grad.abs().sum().item(), 0)



class FullCorpusSamplingTests(unittest.TestCase):
    def test_selection_is_deterministic_and_row_order_independent(self):
        rows = [{'text': f'query {i}', 'positive': f'post {i}', 'negatives': [f'unrelated {i}']}
                for i in range(30)]
        rows.extend(rows[:2])
        class Corpus:
            def __init__(self, source):
                self.source = source
            def rows(self, kind, split):
                self_test.assertEqual((kind, split), ('retrieval', 'train'))
                yield from self.source
        self_test = self
        first, digest = load_pairs_sampled(Corpus(rows), 8)
        second, digest_2 = load_pairs_sampled(Corpus(list(reversed(rows))), 8)
        self.assertEqual(first, second)
        self.assertEqual(digest, digest_2)
        self.assertEqual(len(set(first)), 8)
        self.assertEqual(len(digest), 64)
        with self.assertRaises(ValueError):
            load_pairs_sampled(Corpus(rows), 31)

    def test_text_adam_freezes_nontext_towers_but_trains_projection(self):
        model = PostaMulti(tiny_config(), dropout=0)
        params = text_parameters(model)
        self.assertGreater(len(params), 0)
        names = {name for name, param in model.named_parameters() if param.requires_grad}
        self.assertTrue(any(name.startswith('text_projection.') for name in names))
        self.assertTrue(all(name.startswith(('text_tokens.', 'text_positions.',
                                             'text_encoder.', 'text_norm.',
                                             'text_projection.')) for name in names))
        self.assertFalse(any(param.requires_grad for param in model.image_encoder.parameters()))
        self.assertFalse(any(param.requires_grad for param in model.temporal_encoder.parameters()))
