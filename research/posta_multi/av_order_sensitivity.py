"""Counterfactual frame-order probe for a frozen Posta Multi video encoder.

Reverse *content* over the original chronological timestamp grid. Reuse
already computed frame embeddings: no second image pass and no changes to the
source media. This measures representation sensitivity, not event understanding.
"""
import torch

from av_temporal import aggregate_video_windows


def encode_reversed_video(model, frame_windows, timestamp_windows, *,
                          time_encoding="relative-centered-v1",
                          window_aggregation="hierarchical-residual-v1"):
    if not frame_windows or len(frame_windows) != len(timestamp_windows):
        raise ValueError("matching nonempty frame/timestamp window sequences required")
    batch = frame_windows[0].shape[0]
    dim = model.config.embed_dim
    prev_last = None
    for frames, times in zip(frame_windows, timestamp_windows):
        if (frames.ndim != 3 or frames.shape[0] != batch or
                frames.shape[2] != dim or frames.shape[1] == 0 or
                times.shape != frames.shape[:2] or
                frames.shape[1] > model.config.temporal_length or
                not torch.isfinite(frames).all() or not torch.isfinite(times).all() or
                (times[:, 1:] < times[:, :-1]).any()):
            raise ValueError("invalid frame embedding window or timestamps")
        if prev_last is not None and (times[:, 0] <= prev_last).any():
            raise ValueError("frame timestamps must advance between windows")
        prev_last = times[:, -1]
    # Split the globally reversed stream at the original boundaries.
    # This also works when the final window is shorter than all others.
    reversed_all = torch.cat(frame_windows, dim=1).flip(1)
    offset, reversed_windows, starts = 0, [], []
    for original, times in zip(frame_windows, timestamp_windows):
        count = original.shape[1]
        reversed_chunk = reversed_all[:, offset:offset+count]
        offset += count
        reversed_windows.append(model.encode_video_embeddings(
            reversed_chunk, timestamps=times, time_encoding=time_encoding).float())
        starts.append(times[:, 0])
    return aggregate_video_windows(
        model, torch.stack(reversed_windows, dim=1),
        torch.stack(starts, dim=1),
        mode=window_aggregation, time_encoding=time_encoding)
