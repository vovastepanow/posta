"""Analytical parameter and lower-bound memory accounting for Posta Multi 235M."""
import json
from dataclasses import asdict, dataclass


@dataclass(frozen=True)
class Config:
    vocab_size: int = 65536
    text_width: int = 768
    text_layers: int = 12
    text_heads: int = 12
    text_ff: int = 3072
    text_length: int = 512
    text_pooling: str = 'cls'  # legacy default; 'mean' for the new scratch run
    image_width: int = 768
    image_layers: int = 12
    image_heads: int = 12
    image_ff: int = 3072
    image_size: int = 224
    image_patch: int = 16
    embed_dim: int = 512
    temporal_width: int = 512
    temporal_layers: int = 3
    temporal_heads: int = 8
    temporal_ff: int = 2048
    temporal_length: int = 256
    fusion_layers: int = 1
    fusion_slots: int = 4
    mel_bins: int = 80
    audio_patch_mels: int = 8
    audio_patch_frames: int = 16


def encoder_layer(d, f):
    # PyTorch MHA (QKV/out, biases) + two FF linear layers + two LayerNorms.
    return 4*d*d + 2*d*f + f + 9*d


def parameter_counts(c=Config()):
    if c.text_pooling not in ('cls', 'mean', 'mean-residual'):
        raise ValueError('text_pooling must be cls, mean or mean-residual')
    if c.image_size % c.image_patch or c.mel_bins % c.audio_patch_mels:
        raise ValueError('patch sizes must divide spatial/mel dimensions')
    for width, heads in ((c.text_width,c.text_heads),(c.image_width,c.image_heads),(c.temporal_width,c.temporal_heads)):
        if width % heads:
            raise ValueError('attention width must divide by number of heads')
    if c.embed_dim % 2:
        raise ValueError('embedding dimension must be even for temporal encoding')
    if c.temporal_width != c.embed_dim:
        raise ValueError('temporal width must match embedding dimension')
    patches = (c.image_size // c.image_patch)**2
    counts = {
        'text_token_embedding': c.vocab_size*c.text_width,
        'text_position_embedding': c.text_length*c.text_width,
        'text_transformer': c.text_layers*encoder_layer(c.text_width,c.text_ff),
        'text_final_norm': 2*c.text_width,
        'text_projection': c.text_width*c.embed_dim,
        'image_patch_projection': c.image_width*(3*c.image_patch*c.image_patch+1),
        'image_cls_and_positions': (1+patches+1)*c.image_width,
        'image_transformer': c.image_layers*encoder_layer(c.image_width,c.image_ff),
        'image_final_norm': 2*c.image_width,
        'image_projection': c.image_width*c.embed_dim,
        'temporal_cls_and_positions': (1+c.temporal_length+1)*c.temporal_width,
        'temporal_modality_embedding': 2*c.temporal_width,
        'temporal_transformer': c.temporal_layers*encoder_layer(c.temporal_width,c.temporal_ff),
        'temporal_final_norm': 2*c.temporal_width,
        'audio_patch_projection': (c.audio_patch_mels*c.audio_patch_frames+1)*c.temporal_width,
        'fusion_cls_and_positions': (c.fusion_slots+2)*c.embed_dim,
        'fusion_transformer': c.fusion_layers*encoder_layer(c.embed_dim,c.temporal_ff),
        'fusion_final_norm': 2*c.embed_dim,
        'logit_scale': 1,
    }
    counts['total'] = sum(counts.values())
    return counts


def memory_floor(c=Config()):
    n = parameter_counts(c)['total']
    return {'parameters':n,'weights_fp32_bytes':4*n,'weights_fp16_bytes':2*n,
            'adam_fp32_weights_grad_m_v_bytes':16*n,
            'fp16_weights_grad_plus_fp32_adam_m_v_bytes':12*n,
            'note':'Lower bounds exclude activations, attention, framework, temporary buffers and allocator overhead. FP16 scenario assumes optimizer states offloaded and no FP32 master weights; practical implementations may need them.'}


def main():
    print(json.dumps({'config':asdict(Config()),'parameters':parameter_counts(),
                      'memory_lower_bounds':memory_floor()},indent=2))


if __name__ == '__main__':
    main()
