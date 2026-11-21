"""One-shot lexical BM25 reference for the *existing* local retrieval split.

No training, GPU, model weights, embeddings or media. Only aggregate scores
and sample-selection hashes are written; raw queries/docs remain on the host.
This is NOT a production BM25 service or an independent relevance benchmark.
"""
import argparse
from collections import Counter
import hashlib
import json
import math
from pathlib import Path
import re
import statistics
import unicodedata

from compare_checkpoints import atomic_report
from corpus import Corpus
from ranking_metrics import conservative_rank, summarize_ranks
from retrieval_stage import load_pairs, load_pairs_sampled


def words(value):
    if not isinstance(value, str):
        return []
    return re.findall(r'[^\W_]+', unicodedata.normalize(
        'NFKC', value).casefold(), flags=re.UNICODE)


def bm25_scores(query, indexed, *, k1=1.2, b=0.75):
    """Positive BM25 IDF, deterministic candidates and Unicode tokenization."""
    if not 0 < k1 < 5 or not 0 <= b <= 1:
        raise ValueError('invalid BM25 settings')
    tokens = words(query)
    output = [0.0] * len(indexed)
    if not tokens:
        return output
    n = len(indexed)
    avg_length = sum(len(x) for x in indexed) / max(1, n)
    if not avg_length:
        return output
    docfreq = Counter()
    for x in indexed:
        docfreq.update(set(x))
    for term in tokens:
        df = docfreq[term]
        if not df:
            continue
        idf = math.log1p((n - df + .5)/(df + .5))
        for i, doc in enumerate(indexed):
            count = doc.count(term)
            if count:
                denom = count + k1 * (1-b+b*len(doc)/avg_length)
                output[i] += idf * count * (k1+1)/denom
    return output


def score_pairs(pairs, digest):
    if not pairs:
        raise ValueError('cannot evaluate an empty retrieval pool')
    documents = list(dict.fromkeys(
        doc for _, positive, negative in pairs
        for doc in (positive, negative)))
    docids = {doc: i for i, doc in enumerate(documents)}
    indexed = [words(doc) for doc in documents]
    rank = []
    margins = []
    for query, positive, negative in pairs:
        results = bm25_scores(query, indexed)
        pos = results[docids[positive]]
        neg = results[docids[negative]]
        margins.append(pos-neg)
        rank.append(conservative_rank(results, docids[positive]))
    return {
        'samples': len(pairs),
        'candidate_pool_size': len(documents),
        'validation_pairs_sha256': digest,
        'pair_accuracy': round(sum(x > 0 for x in margins)/len(margins), 6),
        'mean_positive_minus_negative': round(statistics.mean(margins), 6),
        **summarize_ranks(rank),
        'note': ('Lexical BM25 ranks only the observed local candidate pool; '
                 'unlabeled relevant results may exist, other topics/languages differ.'),
    }


def run(corpus, *, split='validation', limit=64):
    if split != 'validation':
        raise ValueError('benchmark test split must not be used for tuning')
    if type(limit) is not int or limit < 1:
        raise ValueError('invalid baseline sample limit')
    # 16 'head' records precisely match the v1/v9 historical validator;
    # separate stable-hash sample gives a less order-sensitive local reference.
    legacy, legacy_hash = load_pairs(corpus, 16, split=split)
    samples, sample_hash = load_pairs_sampled(corpus, limit, split=split)
    return {
        'format': 'posta-multi-bm25-internal-validation-v1',
        'manifest_sha256': corpus.manifest_hash,
        'splits': [split],
        'tokenization': 'unicode-nfkc-casefold-word-v1',
        'bm25_parameters': {'k1': 1.2, 'b': .75},
        'legacy_head16': score_pairs(legacy, legacy_hash),
        'stable_hash': score_pairs(samples, sample_hash),
        'limitations': [
            'Internal local validation; not an independent topic-complete benchmark',
            'BM25 has no semantic paraphrase understanding or visual/audio inputs',
            'Same explicit positives/negatives as existing local retrieval evaluators',
            'No corpus, weights or production data modified; no query text exported',
        ],
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--output', required=True)
    p.add_argument('--samples', type=int, default=64)
    args = p.parse_args()
    path = Path(args.output)
    if path.exists():
        raise FileExistsError('BM25 reference already exists')
    report = run(Corpus(args.dataset), limit=args.samples)
    atomic_report(path, report)
    print(json.dumps({
        'baseline': 'BM25',
        'samples': report['stable_hash']['samples'],
        'legacy_pair_hash': report['legacy_head16']['validation_pairs_sha256'],
        'legacy_mrr': report['legacy_head16']['mean_reciprocal_rank'],
        'stable_mrr': report['stable_hash']['mean_reciprocal_rank'],
    }), flush=True)


if __name__ == '__main__':
    main()
