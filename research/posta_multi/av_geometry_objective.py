"""Geometry-preserving positive-pair alignment (no fabricated negatives).

The reference text encoder is frozen. For aligned media and text targets,
match their *pairwise angular chord distances* in addition to the positive
cosine loss: near-identical media outputs have a large residual when text
targets are diverse. Unlike naive in-batch InfoNCE, unrelated examples are
NOT assumed to be negative documents and false-negative labels are not made.
Use with explicit positive-only objective, not as a replacement for it.
"""
import math

import torch
import torch.nn.functional as F


def geometry_distillation_loss(text, media):
    if (text.ndim != 2 or text.shape != media.shape or
            text.shape[0] < 3 or
            not torch.isfinite(text).all() or not torch.isfinite(media).all()):
        raise ValueError("geometry training requires >=3 finite aligned [B,D] vectors")
    if ((text.float().norm(dim=-1) <= 1e-8).any()
            or (media.float().norm(dim=-1) <= 1e-8).any()):
        raise ValueError("zero-norm geometry vector")
    text_unit = F.normalize(text.float().detach(), dim=-1, eps=1e-8)
    media_unit = F.normalize(media.float(), dim=-1, eps=1e-8)
    # Distances rather than 1-cosine retain meaningful gradients when media
    # outputs are almost identical. Reference distances have no gradients.
    targets = torch.pdist(text_unit, p=2)
    actual = torch.pdist(media_unit, p=2)
    loss = (actual - targets).square().mean()
    if not bool(torch.isfinite(loss)):
        raise ValueError("nonfinite reference geometry loss")
    return loss, {
        "reference_mean_chord": float(targets.detach().mean()),
        "media_mean_chord": float(actual.detach().mean()),
        "mean_absolute_chord_error": float((actual.detach() - targets).abs().mean()),
    }


def alignment_with_geometry(text, media, positive_only_loss, *, geometry_weight=0.5):
    """Wrap the existing positive-only objective, preserving its diagnostics."""
    if (type(geometry_weight) not in (int, float) or
            not math.isfinite(geometry_weight) or not 0 < geometry_weight <= 2):
        raise ValueError("invalid teacher geometry weight")
    existing, stats = positive_only_loss(text, media)
    geom, details = geometry_distillation_loss(text, media)
    return existing + geometry_weight * geom, {
        **stats,
        "teacher_geometry_loss": float(geom.detach()),
        "teacher_geometry_weight": geometry_weight,
        **details,
    }
