"""Recompute frame encodings during backward to bound image activation memory.

The model must remain in eval mode with unchanged parameters throughout the
two phases; eval disables dropout but does not disable gradient computation.
This is a training primitive, not a training loop or evidence of learning.
"""
import torch


def cached_frame_embeddings(model, frames):
    if model.training:
        raise ValueError('frame recomputation requires eval mode')
    if frames.ndim != 5 or frames.shape[1] == 0:
        raise ValueError('frames must be a nonempty [batch, time, channels, height, width] tensor')
    with torch.no_grad():
        embeddings = torch.stack([model.encode_image(frames[:, i])
                                  for i in range(frames.shape[1])], dim=1)
    return embeddings.detach().requires_grad_(True)


def backward_recomputed_frames(model, frames, embeddings):
    if model.training:
        raise ValueError('frame recomputation requires eval mode')
    if embeddings.grad is None or embeddings.shape[:2] != frames.shape[:2]:
        raise ValueError('backpropagate the loss through matching cached embeddings first')
    gradients = embeddings.grad.detach()
    if not torch.isfinite(gradients).all():
        raise ValueError('nonfinite cached embedding gradients')
    for index in range(frames.shape[1]):
        model.encode_image(frames[:, index]).backward(gradients[:, index])
