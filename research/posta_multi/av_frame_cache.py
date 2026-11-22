"""Recompute frame encodings during backward with CPU-backed video inputs.

The frame pixels may remain in host RAM: copy one frame to the model's device
per forward/recompute, not the entire 30fps clip to VRAM. The model must stay
in eval mode with unchanged parameters between cached and replay passes.
This is a memory-bounded training primitive, not evidence of model quality.
"""
import torch


def _validate(model, frames):
    if model.training:
        raise ValueError("frame recomputation requires eval mode")
    if (frames.ndim != 5 or frames.shape[1] == 0 or frames.shape[0] == 0 or
            not torch.is_floating_point(frames)):
        raise ValueError(
            "frames must be a nonempty floating [batch, time, channels, height, width] tensor")
    if frames.requires_grad:
        raise ValueError("pixel gradients are not supported by CPU-backed frame replay")
    return next(model.parameters())


def _one_frame(frames, frame_index, parameter):
    """Transfer only the current frame, preserving the model's dtype/device."""
    return frames[:, frame_index].to(device=parameter.device, dtype=parameter.dtype)


def cached_frame_embeddings(model, frames):
    """Detach frame embeddings into a leaf tensor for temporal gradients."""
    param = _validate(model, frames)
    with torch.no_grad():
        embeddings = torch.stack(
            [model.encode_image(_one_frame(frames, index, param))
             for index in range(frames.shape[1])], dim=1)
    return embeddings.detach().requires_grad_(True)


def backward_recomputed_frames(model, frames, embeddings):
    """Replay one frame at a time, accumulating true image-tower gradients."""
    param = _validate(model, frames)
    if (embeddings.grad is None or embeddings.shape[:2] != frames.shape[:2] or
            embeddings.ndim != 3):
        raise ValueError("backpropagate the loss through matching cached embeddings first")
    gradients = embeddings.grad.detach()
    if not torch.isfinite(gradients).all():
        raise ValueError("nonfinite cached embedding gradients")
    for index in range(frames.shape[1]):
        model.encode_image(_one_frame(frames, index, param)).backward(
            gradients[:, index].to(device=param.device))
