"""Retrieval objective for batches with explicit positive and ignored pairs."""
import torch
import torch.nn.functional as F


def retrieval_loss(queries, candidates, positives, ignored=None, temperature=0.07):
    """Multi-positive contrastive loss, averaged over queries with positives.

    Masks are [queries, candidates]. Ignored pairs are excluded from the
    denominator; callers must mark known duplicates and unverified negatives.
    """
    if queries.ndim != 2 or candidates.ndim != 2 or queries.shape[1] != candidates.shape[1]:
        raise ValueError('embeddings must be matrices with matching dimensions')
    shape = (queries.shape[0], candidates.shape[0])
    if not shape[0] or not shape[1] or positives.shape != shape or positives.dtype != torch.bool:
        raise ValueError('positives must be a nonempty boolean pair mask')
    if ignored is None:
        ignored = torch.zeros_like(positives)
    if ignored.shape != shape or ignored.dtype != torch.bool or (ignored & positives).any():
        raise ValueError('ignored mask must be boolean and disjoint from positives')
    if temperature <= 0:
        raise ValueError('temperature must be positive')
    if not positives.any(dim=1).all():
        raise ValueError('each query must have at least one positive')
    logits = F.normalize(queries.float(), dim=-1) @ F.normalize(candidates.float(), dim=-1).T / temperature
    all_scores = logits.masked_fill(ignored, -torch.inf)
    positive_scores = logits.masked_fill(~positives, -torch.inf)
    return (torch.logsumexp(all_scores, dim=1) - torch.logsumexp(positive_scores, dim=1)).mean()
