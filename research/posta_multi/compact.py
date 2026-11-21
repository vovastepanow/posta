"""Small randomly initialized retrieval backbone for a 3-GB-class GPU.

Separate from the historical 235M checkpoint format. The text encoder is
intentionally compact and trains in FP32 with ordinary AdamW. Image/video/audio
will be aligned later, after text retrieval meets a real benchmark.
"""
from dataclasses import dataclass

import torch
from torch import nn
from torch.nn import functional as F


@dataclass(frozen=True)
class CompactConfig:
    vocab_size: int = 16384
    width: int = 256
    layers: int = 4
    heads: int = 8
    ff_width: int = 768
    max_length: int = 160
    output_dim: int = 192
    dropout: float = 0.1


def validate_config(config):
    if any(type(value) is not int or value < 1 for value in (
            config.vocab_size, config.width, config.layers, config.heads,
            config.ff_width, config.max_length, config.output_dim)):
        raise ValueError('invalid model dimensions')
    if config.vocab_size < 260 or config.width % config.heads != 0 or        config.max_length < 4 or config.output_dim > config.width:
        raise ValueError('incompatible model dimensions')
    if not 0 <= config.dropout < 1:
        raise ValueError('invalid dropout')


class CompactRetriever(nn.Module):
    """Shared query/document tower with content-token mean pooling.

    The encoder is trained from scratch: no teacher or external base weights.
    """
    def __init__(self, config=None):
        super().__init__()
        c = config or CompactConfig()
        validate_config(c)
        self.config = c
        self.tokens = nn.Embedding(c.vocab_size, c.width, padding_idx=0)
        self.positions = nn.Embedding(c.max_length, c.width)
        layer = nn.TransformerEncoderLayer(
            c.width, c.heads, dim_feedforward=c.ff_width,
            dropout=c.dropout, activation='gelu', batch_first=True,
            norm_first=True)
        self.encoder = nn.TransformerEncoder(
            layer, num_layers=c.layers, enable_nested_tensor=False)
        self.norm = nn.LayerNorm(c.width)
        self.project = nn.Linear(c.width, c.output_dim, bias=False)
        self.apply(self._init_weights)
        # nn.TransformerEncoder clones initial layers, so give them distinct
        # attention/FF weights to prevent identical symmetry at initialization.
        for index, item in enumerate(self.encoder.layers):
            with torch.random.fork_rng(devices=[]):
                torch.manual_seed(1701 + index)
                item.apply(self._init_weights)

    @staticmethod
    def _init_weights(module):
        if isinstance(module, (nn.Linear, nn.Embedding)):
            nn.init.normal_(module.weight, mean=0.0, std=0.02)
            if isinstance(module, nn.Linear) and module.bias is not None:
                nn.init.zeros_(module.bias)
        elif isinstance(module, nn.MultiheadAttention):
            nn.init.normal_(module.in_proj_weight, mean=0.0, std=0.02)
            if module.in_proj_bias is not None:
                nn.init.zeros_(module.in_proj_bias)
        elif isinstance(module, nn.LayerNorm):
            nn.init.ones_(module.weight)
            nn.init.zeros_(module.bias)

    def encode(self, tokens, mask):
        c = self.config
        if tokens.ndim != 2 or mask.shape != tokens.shape or tokens.dtype != torch.long:
            raise ValueError('token IDs and attention masks must be [batch,length]')
        if tokens.shape[1] < 3 or tokens.shape[1] > c.max_length:
            raise ValueError('unsupported sequence length')
        valid = mask.bool()
        if not bool(valid[:, 0].all()) or not bool(valid.sum(dim=1).ge(3).all()):
            raise ValueError('CLS, content and SEP must be present')
        if not bool((valid[:, :-1] | ~valid[:, 1:]).all()):
            raise ValueError('padding must follow valid tokens')
        if bool((tokens < 0).any()) or bool((tokens >= c.vocab_size).any()):
            raise ValueError('out-of-vocabulary token')
        pos = torch.arange(tokens.shape[1], device=tokens.device)
        hidden = self.tokens(tokens) + self.positions(pos).unsqueeze(0)
        hidden = self.norm(self.encoder(hidden, src_key_padding_mask=~valid))
        # Remove the CLS and SEP tokens from the pool, avoiding the cold-start
        # CLS-only representation of the previous large model.
        content = valid.clone()
        content[:, 0] = False
        last = valid.long().sum(dim=1) - 1
        content[torch.arange(tokens.shape[0], device=tokens.device), last] = False
        if not bool(content.any(dim=1).all()):
            raise ValueError('empty content token sequence')
        pooled = (hidden * content.unsqueeze(-1)).sum(dim=1) / content.sum(
            dim=1, keepdim=True)
        return F.normalize(self.project(pooled), dim=-1, eps=1e-6)


def paired_contrastive_loss(query, positive, negative, temperature=0.10):
    """One explicit negative per query; batch average, no false in-batch labels."""
    if query.ndim != 2 or query.shape != positive.shape or query.shape != negative.shape:
        raise ValueError('query/positive/negative shapes must match')
    if not 0 < temperature <= 2:
        raise ValueError('invalid contrastive temperature')
    logits = torch.stack(((query * positive).sum(dim=-1),
                          (query * negative).sum(dim=-1)), dim=-1) / temperature
    labels = torch.zeros(len(query), dtype=torch.long, device=query.device)
    return F.cross_entropy(logits.float(), labels)


def trainable_parameters(config):
    """Analytical count, suitable for resource planning without model allocation."""
    validate_config(config)
    w = config.width
    f = config.ff_width
    # 4 attention square matrices; 2 FF matrices, biases, and 2 layer norms
    layer = 4*w*w + 2*w*f + f + 9*w
    return config.vocab_size*w + config.max_length*w +         config.layers*layer + 2*w + w*config.output_dim
