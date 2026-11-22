"""Parameter-free fusion baselines before the Posta Multi fusion tower is trained.

These are explicitly labelled *candidate* baselines: the fusion CLS path has
random weights in the present AV research checkpoints, so a directly aligned
text/image/video/audio embedding must be compared against simpler alternatives
rather than assumed to benefit from random cross-modal attention.

The normalized masked average uses no model weights and can be tested without
GPU. It is NOT an automatic production model replacement or claim of quality.
"""
import math

import torch
import torch.nn.functional as F

DEFAULT_MODALITY_WEIGHTS = (1.0, 1.0, 1.0, 1.0)


def masked_average_fusion(embeddings, modality_mask, weights=DEFAULT_MODALITY_WEIGHTS):
    """Normalize present [text,image,video,audio] slots, then average by weight.

    Inputs: [B, 4, D] floating embeddings, [B, 4] presence mask.
    Missing slots are ignored regardless of their contents; weights must be
    fixed scalar priors, not example-dependent relevance or label leakage.
    """
    if (embeddings.ndim != 3 or embeddings.shape[1] != 4 or
            not torch.is_floating_point(embeddings) or
            modality_mask.shape != embeddings.shape[:2] or
            modality_mask.dtype != torch.bool or
            not modality_mask.any(dim=1).all()):
        raise ValueError("fusion needs [B, 4, D] floats and nonempty [B,4] boolean masks")
    if (len(weights) != 4 or
            any(type(w) not in (int, float) or not math.isfinite(w) or w <= 0
                for w in weights)):
        raise ValueError("four strictly positive finite modality weights required")
    # Mask before checking finiteness: absent modalities may contain sentinel
    # values. Present zero-norm vectors must fail, not silently disappear.
    active = torch.where(modality_mask.unsqueeze(-1), embeddings.float(),
                         torch.zeros_like(embeddings, dtype=torch.float32))
    length = torch.linalg.vector_norm(active, dim=-1)
    if (not torch.isfinite(active).all() or
            (modality_mask & (length <= 1e-8)).any()):
        raise ValueError("present modality has nonfinite or zero embedding")
    scaled = F.normalize(active, dim=-1, eps=1e-8)
    priors = torch.as_tensor(weights, device=embeddings.device, dtype=scaled.dtype)
    effective = modality_mask.to(scaled.dtype) * priors.unsqueeze(0)
    mix = (scaled * effective.unsqueeze(-1)).sum(dim=1)
    if (torch.linalg.vector_norm(mix, dim=-1) <= 1e-8).any():
        raise ValueError("present modality embeddings cancel to zero")
    return F.normalize(mix, dim=-1, eps=1e-8)
