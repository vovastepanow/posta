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



def summarize_aligned_scores(scores):
    """Rank each aligned caption/query against every sampled media candidate.

    Exact tied scores count *against* the selected positive, not as a free win.
    The pool has one labeled positive per row; unlabeled alternative media
    can still be relevant, so these are exploratory local metrics only.
    """
    rows = [list(row) for row in scores]
    n = len(rows)
    if n < 2 or any(len(row) != n for row in rows):
        raise ValueError('aligned similarity scores must be a square matrix >=2')
    ranks = [conservative_rank(row, i) for i, row in enumerate(rows)]
    pair_wins = sum(row[i] > row[(i + 1) % n] for i, row in enumerate(rows))
    return {'wins': pair_wins, 'pair_accuracy': round(pair_wins / n, 4),
            **summarize_ranks(ranks)}
