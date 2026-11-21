"""Experimental retrieval objective with explicit per-query negatives."""
import hashlib
import json
from pathlib import Path
import torch

from corpus import Corpus
from loss import retrieval_loss


def load_pairs(corpus, limit, split='train'):
    if limit < 1:
        raise ValueError('limit must be positive')
    pairs = []
    digest = hashlib.sha256()
    for row in corpus.rows('retrieval', split):
        query, positive = row.get('text'), row.get('positive')
        negatives = row.get('negatives')
        if not isinstance(query, str) or not isinstance(positive, str) or not isinstance(negatives, list):
            continue
        negative = next((x for x in negatives if isinstance(x, str) and x.strip()
                         and x.strip() != positive.strip()), None)
        if not query.strip() or not positive.strip() or negative is None:
            continue
        pair = (query, positive, negative)
        encoded = json.dumps(pair, ensure_ascii=False, separators=(',', ':')).encode()
        digest.update(len(encoded).to_bytes(8, 'big'))
        digest.update(encoded)
        pairs.append(pair)
        if len(pairs) == limit:
            break
    if len(pairs) != limit:
        raise ValueError('not enough retrieval records with explicit negatives')
    return pairs, digest.hexdigest()


def pair_loss(model, tokenizer, pair, device, max_length=128):
    """Only the source's explicit negative is used; no in-batch negatives."""
    tokenizer.enable_truncation(max_length=max_length)
    vectors = []
    for text in pair:
        ids = torch.tensor([tokenizer.encode(text).ids], dtype=torch.long, device=device)
        vectors.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)))
    return retrieval_loss(vectors[0], torch.cat(vectors[1:]),
                          torch.tensor([[True, False]], device=device))
