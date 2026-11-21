"""Small-sample retrieval ranking metrics, with conservative treatment of ties.

These metrics do not establish independent benchmark quality when relevance labels
are incomplete; a different query's positive may be relevant to this query too.
"""
import math


def conservative_rank(scores, positive_index):
    """1-based position, placing the labeled positive last among equal scores."""
    values = list(scores)
    if not values or not isinstance(positive_index, int) or not 0 <= positive_index < len(values):
        raise ValueError('invalid candidate scores or positive index')
    if not all(math.isfinite(float(v)) for v in values):
        raise ValueError('candidate scores must be finite')
    positive = float(values[positive_index])
    return 1 + sum(float(v) >= positive for i, v in enumerate(values) if i != positive_index)


def summarize_ranks(ranks):
    ranks = list(ranks)
    if not ranks or any(not isinstance(rank, int) or rank < 1 for rank in ranks):
        raise ValueError('ranks must be nonempty positive integers')
    n = len(ranks)
    return {
        'recall_at_1': round(sum(rank <= 1 for rank in ranks) / n, 4),
        'recall_at_5': round(sum(rank <= 5 for rank in ranks) / n, 4),
        'recall_at_10': round(sum(rank <= 10 for rank in ranks) / n, 4),
        'mean_reciprocal_rank': round(sum(1 / rank for rank in ranks) / n, 4),
    }
