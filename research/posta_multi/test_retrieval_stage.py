import unittest
import torch
from model import PostaMulti
from synthetic_train import tiny_config
from retrieval_stage import load_pairs, pair_loss


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
