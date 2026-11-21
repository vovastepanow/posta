"""Posta Multi 235M reference architecture; randomly initialized, not a trained model.

Input tokenization, image transforms and audio features are external and must be
versioned with a checkpoint. No production server/database integration.
"""
import math
import torch
from torch import nn
from torch.nn import functional as F

if __package__:
    from .spec import Config
else:
    from spec import Config


def encoder(width, heads, feedforward, layers, dropout):
    layer = nn.TransformerEncoderLayer(width, heads, feedforward, dropout=dropout,
                                       activation='gelu', batch_first=True, norm_first=True)
    return nn.TransformerEncoder(layer, num_layers=layers, enable_nested_tensor=False)


class PostaMulti(nn.Module):
    def __init__(self, config=Config(), dropout=0.1):
        super().__init__()
        c = config
        if c.image_size % c.image_patch or c.mel_bins % c.audio_patch_mels:
            raise ValueError('image and audio patch dimensions must divide input dimensions')
        if c.embed_dim % 2:
            raise ValueError('embedding dimension must be even for temporal encoding')
        if c.temporal_width != c.embed_dim:
            raise ValueError('temporal width must equal shared embedding dimension')
        if c.text_pooling not in ('cls', 'mean', 'mean-residual'):
            raise ValueError('text_pooling must be cls, mean or mean-residual')
        self.config = c
        self.gradient_checkpointing = False
        self.text_tokens = nn.Embedding(c.vocab_size, c.text_width)
        self.text_positions = nn.Embedding(c.text_length, c.text_width)
        self.text_encoder = encoder(c.text_width, c.text_heads, c.text_ff, c.text_layers, dropout)
        self.text_norm = nn.LayerNorm(c.text_width)
        self.text_projection = nn.Linear(c.text_width, c.embed_dim, bias=False)

        self.image_patches = nn.Conv2d(3, c.image_width, c.image_patch, stride=c.image_patch)
        image_tokens = (c.image_size // c.image_patch) ** 2
        self.image_cls = nn.Parameter(torch.empty(1, 1, c.image_width))
        self.image_positions = nn.Parameter(torch.empty(1, image_tokens + 1, c.image_width))
        self.image_encoder = encoder(c.image_width, c.image_heads, c.image_ff, c.image_layers, dropout)
        self.image_norm = nn.LayerNorm(c.image_width)
        self.image_projection = nn.Linear(c.image_width, c.embed_dim, bias=False)

        self.temporal_cls = nn.Parameter(torch.empty(1, 1, c.temporal_width))
        self.temporal_positions = nn.Parameter(torch.empty(1, c.temporal_length + 1, c.temporal_width))
        self.temporal_modality = nn.Embedding(2, c.temporal_width)
        self.temporal_encoder = encoder(c.temporal_width, c.temporal_heads, c.temporal_ff, c.temporal_layers, dropout)
        self.temporal_norm = nn.LayerNorm(c.temporal_width)
        self.audio_patch_projection = nn.Linear(c.audio_patch_mels * c.audio_patch_frames, c.temporal_width)
        self.fusion_cls = nn.Parameter(torch.empty(1, 1, c.embed_dim))
        self.fusion_positions = nn.Parameter(torch.empty(1, c.fusion_slots + 1, c.embed_dim))
        self.fusion_encoder = encoder(c.embed_dim, c.temporal_heads, c.temporal_ff, c.fusion_layers, dropout)
        self.fusion_norm = nn.LayerNorm(c.embed_dim)
        self.logit_scale = nn.Parameter(torch.tensor(math.log(1 / 0.07)))
        self.reset_parameters()

    def reset_parameters(self):
        # Reinitialize each encoder layer independently; do not load pretrained weights.
        for module in self.modules():
            if isinstance(module, (nn.Linear, nn.Embedding, nn.Conv2d)):
                nn.init.normal_(module.weight, std=0.02)
                if getattr(module, 'bias', None) is not None:
                    nn.init.zeros_(module.bias)
            elif isinstance(module, nn.MultiheadAttention):
                nn.init.normal_(module.in_proj_weight, std=0.02)
                if module.in_proj_bias is not None:
                    nn.init.zeros_(module.in_proj_bias)
            elif isinstance(module, nn.LayerNorm):
                nn.init.ones_(module.weight)
                nn.init.zeros_(module.bias)
        for param in (self.image_cls, self.image_positions, self.temporal_cls, self.temporal_positions,
                      self.fusion_cls, self.fusion_positions):
            nn.init.normal_(param, std=0.02)

    def enable_gradient_checkpointing(self, enabled=True):
        """Trade extra computation for lower text-encoder activation memory."""
        self.gradient_checkpointing = bool(enabled)

    def _text_hidden(self, token_ids, attention_mask):
        c = self.config
        if token_ids.ndim != 2 or attention_mask.shape != token_ids.shape:
            raise ValueError('token_ids and attention_mask must have shape [batch, length]')
        if token_ids.shape[1] > c.text_length or token_ids.shape[1] == 0:
            raise ValueError('invalid text sequence length')
        if not attention_mask[:, 0].bool().all():
            raise ValueError('the first token must be an unmasked CLS token')
        pos = torch.arange(token_ids.shape[1], device=token_ids.device)
        x = self.text_tokens(token_ids) + self.text_positions(pos)[None, :, :]
        padding = ~attention_mask.bool()
        if self.training and self.gradient_checkpointing:
            from torch.utils.checkpoint import checkpoint
            # Preserve the exact layers and state_dict keys of legacy 235M.
            for layer in self.text_encoder.layers:
                x = checkpoint(
                    lambda hidden, current_layer=layer: current_layer(
                        hidden, src_key_padding_mask=padding),
                    x, use_reentrant=False, preserve_rng_state=True)
            if self.text_encoder.norm is not None:
                x = self.text_encoder.norm(x)
        else:
            x = self.text_encoder(x, src_key_padding_mask=padding)
        return self.text_norm(x)

    def encode_text(self, token_ids, attention_mask):
        hidden = self._text_hidden(token_ids, attention_mask)
        if self.config.text_pooling == 'cls':
            pooled = hidden[:, 0]
        else:
            # Pool only content, not CLS/SEP/padding. Old checkpoints still
            # default to CLS and retain byte-for-byte parameter compatibility.
            valid = attention_mask.bool().clone()
            valid[:, 0] = False
            last = attention_mask.long().sum(dim=1) - 1
            valid[torch.arange(valid.shape[0], device=valid.device), last] = False
            if not valid.any(dim=1).all():
                raise ValueError('mean pooling requires a content token')
            pooled = (hidden * valid.unsqueeze(-1)).sum(dim=1) / valid.sum(
                dim=1, keepdim=True)
            if self.config.text_pooling == 'mean-residual':
                # Direct lexical route around an almost-constant Transformer
                # representation. This uses the existing trained token
                # embeddings: NO new weights or size change to 235M.
                raw = self.text_tokens(token_ids)
                raw_mean = (raw.float() * valid.unsqueeze(-1)).sum(dim=1) / valid.sum(
                    dim=1, keepdim=True)
                lexical = F.layer_norm(raw_mean, (raw_mean.shape[-1],))
                pooled = pooled + 0.5 * lexical.to(dtype=pooled.dtype)
        return F.normalize(self.text_projection(pooled), dim=-1, eps=1e-6)

    def masked_token_logits(self, token_ids, attention_mask, masked_positions):
        # Tied decoder uses the same randomly initialized token embedding weights.
        if masked_positions.shape != token_ids.shape:
            raise ValueError('masked_positions must match token_ids')
        mask = masked_positions.bool()
        if not mask.any() or mask[:, 0].any() or (mask & ~attention_mask.bool()).any():
            raise ValueError('masked positions must select non-padding tokens other than CLS')
        hidden = self._text_hidden(token_ids, attention_mask)
        return F.linear(hidden[mask], self.text_tokens.weight)

    def encode_image(self, images):
        c = self.config
        if images.ndim != 4 or tuple(images.shape[1:]) != (3, c.image_size, c.image_size):
            raise ValueError('images must have shape [batch, 3, image_size, image_size]')
        x = self.image_patches(images).flatten(2).transpose(1, 2)
        x = torch.cat((self.image_cls.expand(x.shape[0], -1, -1), x), dim=1)
        x = self.image_encoder(x + self.image_positions)
        return F.normalize(self.image_projection(self.image_norm(x[:, 0])), dim=-1, eps=1e-6)

    def _encode_temporal(self, x, modality, mask=None):
        c = self.config
        if x.ndim != 3 or x.shape[1] == 0 or x.shape[1] > c.temporal_length:
            raise ValueError('invalid temporal sequence')
        cls = self.temporal_cls.expand(x.shape[0], -1, -1)
        x = torch.cat((cls, x), dim=1)
        x = x + self.temporal_positions[:, :x.shape[1]] + self.temporal_modality.weight[modality][None, None, :]
        padding = None
        if mask is not None:
            if mask.shape != (x.shape[0], x.shape[1] - 1):
                raise ValueError('temporal mask shape mismatch')
            cls_mask = torch.ones((x.shape[0], 1), dtype=torch.bool, device=x.device)
            padding = ~torch.cat((cls_mask, mask.bool()), dim=1)
        x = self.temporal_encoder(x, src_key_padding_mask=padding)
        return F.normalize(self.temporal_norm(x[:, 0]), dim=-1, eps=1e-6)

    def encode_video(self, frames, frame_mask=None, timestamps=None):
        c = self.config
        if frames.ndim != 5 or tuple(frames.shape[2:]) != (3, c.image_size, c.image_size):
            raise ValueError('frames must have shape [batch, time, 3, image_size, image_size]')
        batch, time = frames.shape[:2]
        if time == 0 or time > c.temporal_length:
            raise ValueError('invalid number of video frames')
        if frame_mask is not None:
            if frame_mask.shape != (batch, time) or not frame_mask[:, 0].bool().all():
                raise ValueError('frame_mask must include the first frame')
        valid = frame_mask.bool() if frame_mask is not None else torch.ones((batch, time), dtype=torch.bool, device=frames.device)
        if ((~valid[:, :-1]) & valid[:, 1:]).any():
            raise ValueError('frame padding must follow valid frames')
        frame_embeddings = self.encode_image(frames.reshape(batch*time, 3, c.image_size, c.image_size)).reshape(batch, time, c.embed_dim)
        if timestamps is not None:
            if timestamps.shape != (batch, time) or not torch.isfinite(timestamps[valid]).all():
                raise ValueError('timestamps must be finite and match frame dimensions')
            relative = (timestamps.float() - timestamps[:, :1].float()).masked_fill(~valid, 0)
            if ((relative[:, 1:] < relative[:, :-1]) & valid[:, 1:] & valid[:, :-1]).any():
                raise ValueError('valid frame timestamps must be nondecreasing')
            frequencies = torch.exp(torch.arange(0, c.embed_dim, 2, device=frames.device, dtype=torch.float32)
                                    * (-math.log(10000.0) / c.embed_dim))
            angles = relative.unsqueeze(-1) * frequencies
            time_encoding = torch.stack((angles.sin(), angles.cos()), dim=-1).flatten(-2)
            frame_embeddings = frame_embeddings + time_encoding.to(frame_embeddings.dtype)
        return self._encode_temporal(frame_embeddings, 0, valid)

    def encode_audio(self, log_mel):
        c = self.config
        if log_mel.ndim != 3 or log_mel.shape[1] != c.mel_bins or log_mel.shape[2] == 0:
            raise ValueError('log_mel must have shape [batch, mel_bins, frames]')
        pad = (-log_mel.shape[2]) % c.audio_patch_frames
        x = F.pad(log_mel, (0, pad)).unsqueeze(1)
        patches = F.unfold(x, kernel_size=(c.audio_patch_mels, c.audio_patch_frames),
                           stride=(c.audio_patch_mels, c.audio_patch_frames)).transpose(1, 2)
        if patches.shape[1] > c.temporal_length:
            raise ValueError('audio has too many patches; segment it before encoding')
        return self._encode_temporal(self.audio_patch_projection(patches), 1)

    def fuse_post(self, embeddings, modality_mask):
        """Fuse fixed slots [text, image, video, audio]; mask is True when present."""
        c = self.config
        if embeddings.ndim != 3 or tuple(embeddings.shape[1:]) != (c.fusion_slots, c.embed_dim):
            raise ValueError('embeddings must have shape [batch, fusion_slots, embed_dim]')
        if modality_mask.shape != embeddings.shape[:2] or not modality_mask.bool().any(dim=1).all():
            raise ValueError('each post needs at least one unmasked modality')
        cls = self.fusion_cls.expand(embeddings.shape[0], -1, -1)
        x = torch.cat((cls, embeddings), dim=1) + self.fusion_positions
        cls_mask = torch.ones((embeddings.shape[0], 1), dtype=torch.bool, device=embeddings.device)
        padding = ~torch.cat((cls_mask, modality_mask.bool()), dim=1)
        x = self.fusion_encoder(x, src_key_padding_mask=padding)
        return F.normalize(self.fusion_norm(x[:, 0]), dim=-1, eps=1e-6)

    def similarity(self, query, candidate):
        return self.logit_scale.clamp(max=math.log(100)).exp() * (query @ candidate.transpose(-1, -2))
