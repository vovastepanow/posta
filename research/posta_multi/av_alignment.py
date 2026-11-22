"""Memory-bounded video-caption and speech-transcript alignment primitives.

Video descriptions are weak video labels, not labels of every sound in the
clip. Speech transcripts supervise speech, not general audio understanding.
This module does not load data, choose negatives, save or promote weights.
"""
import torch
import torch.nn.functional as F

from av_frame_cache import cached_frame_embeddings, backward_recomputed_frames
from av_temporal import aggregate_video_windows
from run001_retrieval_loss import positive_only_alignment_loss


def configure_av_trainable(model):
    """Freeze text and fusion; train image, temporal and audio paths only."""
    prefixes = ('image_', 'temporal_', 'audio_patch_projection.')
    for name, parameter in model.named_parameters():
        parameter.requires_grad_(name.startswith(prefixes))
    model.eval()  # Required for deterministic frame recomputation.


def _video(model, windows, device, frame_caches, time_encoding, window_aggregation):
    if not windows:
        raise ValueError('a video needs at least one nonempty window')
    vectors, starts = [], []
    for frames, timestamps in windows:
        # Retain the video pixels in host RAM. cached_frame_embeddings and its
        # backward replay transfer one frame at a time to the GPU; otherwise
        # every 30fps frame from all videos remains resident until backward.
        if frames.requires_grad:
            raise ValueError('video pixel gradients are unsupported')
        frames = frames.detach().to(device='cpu', dtype=torch.float32)
        timestamps = timestamps.to(device=device, dtype=torch.float32)
        cached = cached_frame_embeddings(model, frames)
        vectors.append(model.encode_video_embeddings(cached, timestamps=timestamps,
                                                     time_encoding=time_encoding).float())
        starts.append(timestamps[:, 0])
        frame_caches.append((frames, cached))
    return aggregate_video_windows(model, torch.stack(vectors, dim=1), torch.stack(starts, dim=1),
                                   mode=window_aggregation, time_encoding=time_encoding).squeeze(0)


def _speech(model, mel, device):
    if mel.ndim != 3 or mel.shape[0] != 1 or mel.shape[-1] == 0:
        raise ValueError('speech mel must be a nonempty single example')
    c = model.config
    capacity = (c.temporal_length // (c.mel_bins // c.audio_patch_mels)) * c.audio_patch_frames
    if capacity < 1:
        raise ValueError('audio temporal capacity is invalid')
    vectors = []
    for part in mel.split(capacity, dim=-1):
        part = part.to(device=device, dtype=next(model.parameters()).dtype)
        vectors.append(model.encode_audio(part).float())
    return F.normalize(torch.stack(vectors).mean(dim=0), dim=-1, eps=1e-6).squeeze(0)


def alignment_backward(model, video_pairs, speech_pairs, device, optimizer=None,
                       time_encoding='legacy', window_aggregation='mean'):
    """Backpropagate labeled positives, then recompute frames one at a time.

    Each pair is (frozen_text_vector, windows_or_speech_mel); every supplied
    kind needs >=2 examples. Caller zeroes gradients and steps the optimizer.
    For scaled FP16, optimizer.backward must implement the scale consistently.
    """
    if model.training:
        raise ValueError('alignment requires eval mode for deterministic frame replay')
    if not video_pairs and not speech_pairs:
        raise ValueError('at least one labeled media kind is required')
    if any(pairs and len(pairs) < 2 for pairs in (video_pairs, speech_pairs)):
        raise ValueError('each supplied media kind needs at least two pairs')
    caches, losses, diagnostics = [], [], {}
    for kind, pairs in (('video', video_pairs), ('speech', speech_pairs)):
        if not pairs:
            continue
        targets, predictions = [], []
        for text, media in pairs:
            if text.requires_grad or text.ndim != 1 or text.numel() != model.config.embed_dim:
                raise ValueError('text target must be a detached embedding')
            targets.append(text.to(device=device, dtype=torch.float32))
            predictions.append(_video(model, media, device, caches, time_encoding, window_aggregation) if kind == 'video'
                               else _speech(model, media, device))
        loss, stats = positive_only_alignment_loss(torch.stack(targets), torch.stack(predictions))
        losses.append(loss)
        diagnostics[kind] = stats
    total = torch.stack(losses).mean()
    if optimizer is None:
        total.backward()
    else:
        optimizer.backward(total)
    for frames, cached in caches:
        backward_recomputed_frames(model, frames, cached)
    return float(total.detach()), diagnostics
