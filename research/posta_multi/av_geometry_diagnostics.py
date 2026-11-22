"""Reference-vs-media geometry diagnostics without invented negative labels.

The *text* vectors are supervision anchors, not proof of semantic correctness.
Media vectors from different posts may legitimately be similar; this module
reports their pairwise geometry relative to matched texts instead of declaring
all other examples irrelevant. It is a read-only diagnostic, not an objective.
"""
import torch
import torch.nn.functional as F


def pairwise_geometry(text_vectors, media_vectors):
    if (text_vectors.ndim != 2 or text_vectors.shape != media_vectors.shape
            or text_vectors.shape[0] < 3 or
            not torch.isfinite(text_vectors).all()
            or not torch.isfinite(media_vectors).all()):
        raise ValueError("at least three finite aligned [N,D] text/media vectors required")
    t = F.normalize(text_vectors.float(), dim=-1, eps=1e-8)
    m = F.normalize(media_vectors.float(), dim=-1, eps=1e-8)
    if ((text_vectors.float().norm(dim=-1) <= 1e-8).any()
            or (media_vectors.float().norm(dim=-1) <= 1e-8).any()):
        raise ValueError("zero-norm aligned embedding")
    offdiag = torch.triu_indices(t.shape[0], t.shape[0], offset=1, device=t.device)
    t_sims = (t @ t.T)[offdiag[0], offdiag[1]]
    m_sims = (m @ m.T)[offdiag[0], offdiag[1]]
    # A collapsed media encoder cannot preserve variations in teacher geometry.
    gap = m_sims - t_sims
    residual = m_sims - m_sims.mean()
    reference = t_sims - t_sims.mean()
    scale = reference.norm() * residual.norm()
    correlation = (float((reference * residual).sum() / scale)
                   if float(scale) > 1e-8 else None)
    return {
        "samples": t.shape[0],
        "sample_pairs": t_sims.numel(),
        "text_interexample_cosine": float(t_sims.mean()),
        "text_interexample_min_cosine": float(t_sims.min()),
        "text_interexample_max_cosine": float(t_sims.max()),
        "text_interexample_cosine_std": float(t_sims.std(unbiased=False)),
        "media_interexample_cosine": float(m_sims.mean()),
        "media_interexample_min_cosine": float(m_sims.min()),
        "media_interexample_max_cosine": float(m_sims.max()),
        "media_interexample_cosine_std": float(m_sims.std(unbiased=False)),
        "mean_media_minus_text_cosine": float(gap.mean()),
        "mean_absolute_geometry_error": float(gap.abs().mean()),
        "geometry_pearson_correlation": correlation,
        "interpretation": (
            "Teacher geometry is a diagnostic reference, not ground-truth relevance; "
            "different examples are not assumed to be negative pairs."
        ),
    }
