"""Order-aware aggregation of bounded video-window embeddings."""
import torch
import torch.nn.functional as F


def aggregate_video_windows(model, windows, timestamps, *, mode='mean', time_encoding='legacy'):
    """Aggregate [batch, windows, dim] in chronological order.

    Hierarchical mode reuses existing temporal weights; it adds no parameters.
    Long sequences are recursively reduced in groups bounded by temporal_length.
    It is a trainable mechanism, not evidence that event order has been learned.
    """
    if (windows.ndim != 3 or windows.shape[1] == 0 or
            windows.shape[2] != model.config.embed_dim or
            timestamps.shape != windows.shape[:2] or
            not torch.isfinite(windows).all() or not torch.isfinite(timestamps).all() or
            (timestamps[:, 1:] < timestamps[:, :-1]).any()):
        raise ValueError('invalid chronological video window sequence')
    if mode == 'mean':
        return F.normalize(windows.float().mean(dim=1), dim=-1, eps=1e-6)
    if mode not in ('hierarchical-v1', 'hierarchical-residual-v1'):
        raise ValueError('unknown video window aggregation mode')
    capacity = model.config.temporal_length
    if capacity < 2:
        raise ValueError('hierarchical aggregation requires temporal capacity of at least two')
    while windows.shape[1] > 1:
        reduced, reduced_times = [], []
        for offset in range(0, windows.shape[1], capacity):
            chunk = windows[:, offset:offset+capacity]
            chunk_times = timestamps[:, offset:offset+capacity]
            if chunk.shape[1] == 1:
                encoded = chunk[:, 0]
            else:
                chunk = chunk.to(dtype=next(model.parameters()).dtype)
                encoded = model.encode_video_embeddings(chunk, timestamps=chunk_times,
                                                        time_encoding=time_encoding)
                if mode == 'hierarchical-residual-v1':
                    skip = F.normalize(chunk.float().mean(dim=1), dim=-1, eps=1e-6)
                    encoded = F.normalize(encoded.float() + 2 * skip, dim=-1, eps=1e-6)
            reduced.append(encoded)
            reduced_times.append(chunk_times[:, 0])
        windows = torch.stack(reduced, dim=1)
        timestamps = torch.stack(reduced_times, dim=1)
    return F.normalize(windows[:, 0].float(), dim=-1, eps=1e-6)
