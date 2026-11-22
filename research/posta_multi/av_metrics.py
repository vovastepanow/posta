"""Small-pool paired-media retrieval diagnostics; not a general quality score."""
import torch
import torch.nn.functional as F


def paired_media_metrics(text_vectors, media_vectors):
    if (text_vectors.ndim != 2 or text_vectors.shape != media_vectors.shape or
            text_vectors.shape[0] < 2 or
            not torch.isfinite(text_vectors).all() or not torch.isfinite(media_vectors).all()):
        raise ValueError('need at least two aligned finite embedding pairs')
    text = F.normalize(text_vectors.float(), dim=-1, eps=1e-8)
    media = F.normalize(media_vectors.float(), dim=-1, eps=1e-8)
    scores = text @ media.T
    positive = scores.diagonal()
    # Ties rank behind all other candidates, avoiding optimistic random ties.
    ranks = (scores >= positive[:, None]).sum(dim=1)
    off_diagonal = ~torch.eye(len(scores), dtype=torch.bool, device=scores.device)
    return {'pairs': len(scores), 'hit_at_1': float((ranks == 1).float().mean()),
            'mean_reciprocal_rank': float((1.0 / ranks.float()).mean()),
            'mean_positive_cosine': float(positive.mean()),
            'mean_other_cosine': float(scores[off_diagonal].mean()),
            'media_interexample_cosine': float((media @ media.T)[off_diagonal].mean())}
