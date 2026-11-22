"""Deterministic reciprocal-rank fusion for internal retrieval experiments.

Each rank is conservative on exact ties. Candidate sets must be aligned;
neither score scale is assumed to match the other.
"""
import math

from ranking_metrics import conservative_rank, summarize_ranks


def conservative_ranks(scores):
    values = [float(value) for value in scores]
    if not values or not all(math.isfinite(value) for value in values):
        raise ValueError('candidate scores must be finite and nonempty')
    ordered = sorted(values, reverse=True)
    last_positions = {value: i + 1 for i, value in enumerate(ordered)}
    return [last_positions[value] for value in values]


def fuse_scores(lexical, semantic, *, semantic_weight, rank_constant=60):
    if len(lexical) != len(semantic) or not lexical:
        raise ValueError('candidate pools must be identical and nonempty')
    if (isinstance(semantic_weight, bool) or not isinstance(semantic_weight, (int, float)) or
            not math.isfinite(semantic_weight) or not 0 <= semantic_weight <= 1 or
            type(rank_constant) is not int or rank_constant < 1):
        raise ValueError('invalid fusion settings')
    left, right = conservative_ranks(lexical), conservative_ranks(semantic)
    return [(1-semantic_weight)/(rank_constant+a) + semantic_weight/(rank_constant+b)
            for a, b in zip(left, right)]


def evaluate_rows(rows, weight):
    if not rows:
        raise ValueError('no validation queries')
    ranks, wins = [], 0
    for row in rows:
        values = fuse_scores(row['lexical'], row['semantic'], semantic_weight=weight)
        pos, neg = row['positive'], row['negative']
        ranks.append(conservative_rank(values, pos))
        wins += values[pos] > values[neg]
    return {'samples': len(rows), 'pair_accuracy': round(wins / len(rows), 6),
            **summarize_ranks(ranks)}


def compare_splits(rows, calibration_size, weights=(0.0, 0.25, 0.5, 0.75, 1.0)):
    """Choose weight on the prefix, evaluate it on the remaining queries."""
    if type(calibration_size) is not int or not 1 <= calibration_size < len(rows):
        raise ValueError('calibration and evaluation queries must both be present')
    if not weights or len(set(weights)) != len(weights):
        raise ValueError('fusion weights must be unique and nonempty')
    calibration, heldout = rows[:calibration_size], rows[calibration_size:]
    curves = {weight: evaluate_rows(calibration, weight) for weight in weights}
    # Lower semantic weight wins exact ties, avoiding an unjustified change.
    chosen = min(weights, key=lambda weight: (-curves[weight]['mean_reciprocal_rank'], weight))
    return {'calibration_samples': len(calibration),
            'evaluation_samples': len(heldout),
            'selected_semantic_weight': chosen,
            'calibration': {str(weight): curves[weight] for weight in weights},
            'evaluation': {'bm25': evaluate_rows(heldout, 0.0),
                           'semantic': evaluate_rows(heldout, 1.0),
                           'selected_hybrid': evaluate_rows(heldout, chosen)}}
