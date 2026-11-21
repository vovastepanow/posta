"""Anti-collapse retrieval supervision for Posta Multi Run 001.

All negatives must be explicitly curated for the corresponding query.
No cross-query in-batch negatives: other posts may be relevant but unlabeled.
This objective is independent of legacy v1-v9 checkpoint formats.
"""
import math

import torch
import torch.nn.functional as F


def _validated(query, positive, negative):
    if (query.ndim != 2 or query.shape != positive.shape or
            query.shape != negative.shape or query.shape[0] < 2):
        raise ValueError('need at least two aligned query/positive/negative embeddings')
    if not all(bool(torch.isfinite(x).all()) for x in (query, positive, negative)):
        raise ValueError('embeddings must be finite')


def _interexample_similarity(vectors):
    vectors = F.normalize(vectors.float(), dim=-1, eps=1e-8)
    n = vectors.shape[0]
    sim = vectors @ vectors.T
    return sim[~torch.eye(n, device=vectors.device, dtype=torch.bool)]


def _variance_floor(vectors, minimum_std=0.035):
    # Shared gradients across distinct texts, unlike plain per-query CE.
    # A near-collapsed batch has tiny variance even if all pairwise logits tie.
    v = F.normalize(vectors.float(), dim=-1, eps=1e-8)
    std = torch.sqrt(v.var(dim=0, unbiased=False) + 1e-6)
    return F.relu(minimum_std - std).mean()


def anti_collapse_loss(query, positive, negative, *,
                       temperature=0.10, margin=0.10,
                       variance_weight=5.0, repulsion_weight=0.5,
                       repulsion_threshold=0.80, minimum_std=0.035):
    """Supervised margin + inter-example variance and diversity.

    Returns scalar loss and detached scalar diagnostics. All embedding
    statistics are normalized cosine and calculated in FP32.
    """
    _validated(query, positive, negative)
    params = (temperature, margin, variance_weight, repulsion_weight,
              repulsion_threshold, minimum_std)
    if any(not isinstance(x, (float, int)) or not math.isfinite(x) for x in params):
        raise ValueError('objective hyperparameters must be finite')
    if not 0 < temperature <= 2 or margin < 0 or variance_weight < 0 or \
            repulsion_weight < 0 or not -1 < repulsion_threshold < 1 or \
            not 0 < minimum_std < 1:
        raise ValueError('invalid anti-collapse hyperparameters')

    q = F.normalize(query.float(), dim=-1, eps=1e-8)
    p = F.normalize(positive.float(), dim=-1, eps=1e-8)
    n = F.normalize(negative.float(), dim=-1, eps=1e-8)
    good = (q * p).sum(-1)
    bad = (q * n).sum(-1)
    separation = good - bad
    pairwise = F.softplus((margin - separation) / temperature).mean()
    # Different *queries* and *positive documents* should remain distinguishable.
    # Explicit negatives are already handled per query; do not declare other
    # queries to be irrelevant, only discourage their exact collapse.
    q_same = _interexample_similarity(q)
    p_same = _interexample_similarity(p)
    diversity = (_variance_floor(q, minimum_std) +
                 _variance_floor(p, minimum_std)) * 0.5
    repulsion = (F.relu(q_same - repulsion_threshold).square().mean() +
                 F.relu(p_same - repulsion_threshold).square().mean()) * 0.5
    result = pairwise + variance_weight * diversity + repulsion_weight * repulsion
    if not bool(torch.isfinite(result)):
        raise RuntimeError('nonfinite anti-collapse objective')
    measurements = {
        'pair_loss': float(pairwise.detach()),
        'variance_penalty': float(diversity.detach()),
        'repulsion_penalty': float(repulsion.detach()),
        'pair_accuracy': float((separation.detach() > 0).float().mean()),
        'mean_margin': float(separation.detach().mean()),
        'query_interexample_cosine': float(q_same.detach().mean()),
        'document_interexample_cosine': float(p_same.detach().mean()),
    }
    return result, measurements


def checkpoint_is_healthy(metrics, *, max_interexample_cosine=0.97,
                          min_pair_accuracy=0.55, min_mean_margin=0.005):
    """Fail closed on collapse, even when training loss decreased."""
    required = ('pair_accuracy', 'mean_positive_minus_negative',
                'query_interexample_cosine', 'document_interexample_cosine',
                'mean_reciprocal_rank')
    if not isinstance(metrics, dict):
        return False
    values = [metrics.get(name) for name in required]
    if any(isinstance(x, bool) or not isinstance(x, (int, float)) or
           not math.isfinite(x) for x in values):
        return False
    return (metrics['pair_accuracy'] >= min_pair_accuracy and
            metrics['mean_positive_minus_negative'] >= min_mean_margin and
            metrics['query_interexample_cosine'] <= max_interexample_cosine and
            metrics['document_interexample_cosine'] <= max_interexample_cosine and
            0 <= metrics['mean_reciprocal_rank'] <= 1)



def positive_only_alignment_loss(query, positive, *,
                                 variance_weight=5.0, repulsion_weight=0.5,
                                 repulsion_threshold=0.80, minimum_std=0.035):
    """Positive-only supervision; never invent negatives for unlabeled posts.

    Contrastive alignment alone can collapse to constant vectors. Retain the
    same per-dimension variance floor and inter-example repulsion as the
    explicit-negative objective. At least two distinct query pairs required.
    """
    _validated(query, positive, positive)
    hyper = (variance_weight, repulsion_weight, repulsion_threshold, minimum_std)
    if any(not isinstance(x, (int, float)) or not math.isfinite(x) for x in hyper):
        raise ValueError('positive-only loss weights must be finite')
    if variance_weight < 0 or repulsion_weight < 0 or not -1 < repulsion_threshold < 1 or not 0 < minimum_std < 1:
        raise ValueError('invalid positive-only objective')
    q = F.normalize(query.float(), dim=-1, eps=1e-8)
    p = F.normalize(positive.float(), dim=-1, eps=1e-8)
    alignment = (1 - (q*p).sum(-1)).mean()
    variance = (_variance_floor(q, minimum_std) +
                _variance_floor(p, minimum_std))*0.5
    separation = (_interexample_similarity(q), _interexample_similarity(p))
    repulsion = sum(F.relu(x - repulsion_threshold).square().mean()
                    for x in separation) * 0.5
    loss = alignment + variance_weight*variance + repulsion_weight*repulsion
    if not bool(torch.isfinite(loss)):
        raise RuntimeError('nonfinite positive-only objective')
    diagnostics = {
        'alignment_loss': float(alignment.detach()),
        'variance_penalty': float(variance.detach()),
        'repulsion_penalty': float(repulsion.detach()),
        'query_interexample_cosine': float(separation[0].detach().mean()),
        'document_interexample_cosine': float(separation[1].detach().mean()),
    }
    return loss, diagnostics
