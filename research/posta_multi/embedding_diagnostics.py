"""Frozen embedding geometry and alignment diagnostics for Posta Multi 235M.

Research-only. The functions do not assert model quality; use fixed held-out
examples with validated provenance and compare to lexical/random baselines.
"""
import math

import torch
import torch.nn.functional as F


def _matrix(vectors, name):
    x = torch.as_tensor(vectors, dtype=torch.float64, device="cpu")
    if x.ndim != 2 or x.shape[0] < 2 or x.shape[1] < 2:
        raise ValueError(f"{name} needs at least two samples and dimensions")
    if not torch.isfinite(x).all() or (x.norm(dim=-1) <= 1e-12).any():
        raise ValueError(f"{name} contains nonfinite or zero vectors")
    return F.normalize(x, dim=1)


def geometry(vectors):
    """Detect anisotropy/collapse without relying on arbitrary cosine cutoffs."""
    x = _matrix(vectors, "vectors")
    n = x.shape[0]
    cosines = x @ x.T
    pairwise = cosines[~torch.eye(n, dtype=torch.bool)]
    centered = x - x.mean(dim=0, keepdim=True)
    eigenvalues = torch.linalg.eigvalsh(centered @ centered.T).clamp_min_(0)
    total = eigenvalues.sum()
    effective_rank = (float(total.square() / eigenvalues.square().sum())
                      if float(total) > 1e-15 else 0.0)
    return {
        "count": n,
        "off_diagonal_mean_cosine": float(pairwise.mean()),
        "off_diagonal_median_cosine": float(pairwise.median()),
        "off_diagonal_p05_cosine": float(torch.quantile(pairwise, 0.05)),
        "off_diagonal_p95_cosine": float(torch.quantile(pairwise, 0.95)),
        "off_diagonal_fraction_over_0_995": float((pairwise > 0.995).double().mean()),
        "centered_effective_rank": effective_rank,
        "centered_rank_fraction": effective_rank / min(n - 1, x.shape[1]),
    }


def _tie_aware_metrics(scores):
    """Expected retrieval score under uniform tie-breaking, not row-index order."""
    n = scores.shape[0]
    gold = scores.diag().unsqueeze(1)
    equal = torch.isclose(scores, gold, atol=1e-8, rtol=0)
    greater = (scores > gold) & ~equal
    first_rank = 1 + greater.sum(dim=1).to(torch.float64)
    ties = equal.sum(dim=1).to(torch.float64)
    result = {}
    for k in (1, 5):
        chance = ((k - first_rank + 1).clamp(min=0) / ties).clamp(max=1)
        result[f"recall_at_{k}"] = float(chance.mean())
    result["mrr"] = sum(
        sum(1 / (int(first) + offset) for offset in range(int(size))) / int(size)
        for first, size in zip(first_rank.tolist(), ties.tolist())
    ) / n
    off_diagonal = ~torch.eye(n, dtype=torch.bool)
    hard_negatives = scores.masked_fill(~off_diagonal, -math.inf).max(dim=1).values
    result["mean_positive_minus_hardest_negative"] = float(
        (scores.diag() - hard_negatives).mean())
    return result


def paired_alignment(left, right):
    """Measure two-way matching assuming aligned rows: pair i matches pair i.

    Duplicates/semantically equivalent candidates must be removed or masked
    upstream; this function is only a diagnostic, not a relevance benchmark.
    """
    left, right = _matrix(left, "left"), _matrix(right, "right")
    if left.shape != right.shape:
        raise ValueError("paired embeddings need identical [N, D] shape")
    scores = left @ right.T
    return {"count": left.shape[0],
            "left_to_right": _tie_aware_metrics(scores),
            "right_to_left": _tie_aware_metrics(scores.T)}


def transformation_sensitivity(before, after):
    """Compare each video's vector with its time-reversed or shuffled version.

    A high number does not prove correct temporal reasoning; a near-zero number
    is evidence that this particular perturbation was almost ignored.
    """
    before, after = _matrix(before, "before"), _matrix(after, "after")
    if before.shape != after.shape:
        raise ValueError("transformation embeddings need identical [N, D] shape")
    cosines = (before * after).sum(dim=1).clamp(-1, 1)
    return {"count": before.shape[0],
            "mean_1_minus_cosine": float((1 - cosines).mean()),
            "max_1_minus_cosine": float((1 - cosines).max())}
