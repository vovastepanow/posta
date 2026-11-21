"""Read-only geometry diagnostics for text-to-text retrieval embeddings.

Useful to spot a candidate collapse or weak positive/negative separation,
not to assert causal effects or absolute model quality.
"""
import math
import statistics

import torch
import torch.nn.functional as F


def off_diagonal_cosine(vectors):
    """Mean cosine of *different* rows. Close to 1 can indicate collapse."""
    if vectors.ndim != 2 or vectors.shape[0] < 2:
        raise ValueError('need at least two embedding rows')
    if not bool(torch.isfinite(vectors).all()):
        raise ValueError('embeddings must be finite')
    vectors = F.normalize(vectors.float(), dim=1, eps=1e-8)
    indices = torch.triu_indices(len(vectors), len(vectors), offset=1)
    return round(float((vectors @ vectors.T)[indices[0], indices[1]].mean()), 6)


def retrieval_health(query_embeddings, document_embeddings, positives, negatives):
    """Aggregated diagnostics only: never expose queries or source documents.

    positives/negatives are explicit cosine similarities for every validation
    query. The numbers are calculated from *the same* query/positive/negative
    pairs across checkpoints, provided their selected-pairs SHA matches.
    """
    if query_embeddings.ndim != 2 or document_embeddings.ndim != 2 or \
       query_embeddings.shape[1] != document_embeddings.shape[1]:
        raise ValueError('query/document vectors have inconsistent dimensions')
    if query_embeddings.shape[0] < 2 or document_embeddings.shape[0] < 2:
        raise ValueError('need multiple independent query and document vectors')
    if len(positives) != len(negatives) or not positives:
        raise ValueError('need corresponding nonempty positive and negative similarities')
    values = [*positives, *negatives]
    if not all(math.isfinite(float(x)) for x in values):
        raise ValueError('similarity scores must be finite')
    margins = [float(p) - float(n) for p, n in zip(positives, negatives)]
    return {
        'mean_positive_cosine': round(statistics.mean(positives), 6),
        'mean_explicit_negative_cosine': round(statistics.mean(negatives), 6),
        'mean_positive_minus_negative': round(statistics.mean(margins), 6),
        'median_margin': round(statistics.median(margins), 6),
        'margin_std': round(statistics.pstdev(margins), 6),
        'min_margin': round(min(margins), 6),
        'max_margin': round(max(margins), 6),
        'fraction_positive_margin': round(sum(m > 0 for m in margins) / len(margins), 6),
        'query_interexample_cosine': off_diagonal_cosine(query_embeddings),
        'document_interexample_cosine': off_diagonal_cosine(document_embeddings),
        'note': ('Values near 1 for interexample cosine may indicate embedding collapse; '
                 'this is only a tiny local validation pool and not causal proof.'),
    }
