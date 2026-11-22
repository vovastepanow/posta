"""Experimental word/character lexical index over individual post text.

The fixed weight was selected on local validation and evaluated once on the
frozen NanoMIRACL subset. This component does not consume model weights or
source/channel categories. It is an in-memory research implementation, not
yet a production-scale index.
"""
from hybrid_retrieval import fuse_scores
from nanomiracl_benchmark import BM25Index
from run001_lexical_validation import CharacterBM25


class LexicalFusionIndex:
    def __init__(self, posts):
        if (not isinstance(posts, dict) or not posts or
                any(not isinstance(key, str) or not key or
                    not isinstance(value, str) for key, value in posts.items())):
            raise ValueError('posts must be a nonempty mapping of post IDs to text')
        self._word = BM25Index(posts)
        self._character = CharacterBM25([posts[key] for key in self._word.ids])

    def search(self, query, *, top_k=100):
        if not isinstance(query, str):
            raise ValueError('query must be text')
        if type(top_k) is not int or not 1 <= top_k <= 10000:
            raise ValueError('top_k must be between 1 and 10000')
        word = self._word.scores(query)
        character = self._character.scores(query)
        if not any(word) and not any(character):
            return []
        fused = fuse_scores(word, character, semantic_weight=0.5, rank_constant=60)
        ranked = sorted((i for i in range(len(fused)) if word[i] > 0 or character[i] > 0),
                        key=lambda i: (-fused[i], self._word.ids[i]))
        return [{'post_id': self._word.ids[i], 'score': fused[i]} for i in ranked[:top_k]]
