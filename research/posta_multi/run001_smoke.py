"""Posta Multi 235M / Run 001: one *synthetic* text-training GPU smoke step.

The complete 235,179,777-parameter multimodal model is initialized from
random weights. Only the text tower is trained, on synthetic token IDs.
No real dataset, tokenizer or prior checkpoint is read. No weights are saved.
This probes a GTX 1060-class device within a 45% VRAM allocator cap.
"""
import argparse
import json
import math
from dataclasses import asdict, replace
from pathlib import Path

import torch
import torch.nn.functional as F

from model import PostaMulti
from spec import Config, parameter_counts

RUN_ID = 'posta-multi-235m-run-001'
TEXT_NAMES = ('text_tokens', 'text_positions', 'text_encoder',
              'text_norm', 'text_projection')


def trainable_text_parameters(model):
    """Freeze every non-text parameter, including the unused multimodal towers."""
    result = []
    for name, parameter in model.named_parameters():
        enabled = any(name == prefix or name.startswith(prefix + '.') for prefix in TEXT_NAMES)
        parameter.requires_grad_(enabled)
        if enabled:
            result.append(parameter)
    return result


def synthetic_masked_step(model, optimizer, device='cpu', *, length=24,
                          max_masked=1):
    """One training update, with bounded MLM logits and real gradient flow.

    Model must already be on device. CPU smoke uses AdamW/FP32;
    GPU smoke uses the FP16/CPU-master optimizer supplied by the caller.
    """
    if length < 6 or length > model.config.text_length or \
       type(max_masked) is not int or not 1 <= max_masked <= length - 2:
        raise ValueError('invalid synthetic sequence length/masking budget')
    device = torch.device(device)
    model.train()
    model.enable_gradient_checkpointing(True)
    token_ids = torch.randint(4, model.config.vocab_size, (1, length),
                              dtype=torch.long, device=device)
    token_ids[0, 0] = 1
    token_ids[0, -1] = 2
    selected = torch.zeros_like(token_ids, dtype=torch.bool)
    selected[0, 1:1+max_masked] = True
    target = token_ids[selected].clone()
    token_ids[selected] = 3
    mask = torch.ones_like(token_ids, dtype=torch.bool)
    optimizer.zero_grad()
    loss = F.cross_entropy(
        model.masked_token_logits(token_ids, mask, selected).float(), target)
    if not bool(torch.isfinite(loss)):
        raise RuntimeError('non-finite synthetic loss')
    optimizer.backward(loss) if hasattr(optimizer, 'backward') else loss.backward()
    parameters = [p for p in model.parameters() if p.requires_grad]
    nonzero_grad = sum(int(p.grad is not None and
                           bool(torch.isfinite(p.grad).all()) and
                           bool(p.grad.detach().abs().max() > 0)) for p in parameters)
    if nonzero_grad < 3:
        raise RuntimeError('text gradient missing or nonfinite')
    # MLM predicts with tied token embeddings, not text_projection. Verify a
    # parameter actually used by this objective, otherwise the smoke would
    # incorrectly reject a valid MLM step.
    before = model.text_tokens.weight.detach().float().cpu().clone()
    if optimizer.step() is False:
        raise RuntimeError('optimizer rejected nonfinite gradient')
    changed = bool((model.text_tokens.weight.detach().float().cpu() - before).abs().max() > 0)
    if not changed:
        raise RuntimeError('optimizer step did not update text token embeddings')
    return {'loss': round(float(loss.detach().cpu()), 6),
            'nonzero_gradient_tensors': nonzero_grad,
            'updated_text_embeddings': True}


def build_model(config, device):
    """Order matters: configure CUDA fraction before any model transfer."""
    device = torch.device(device)
    if device.type == 'cuda':
        torch.cuda.set_per_process_memory_fraction(0.45)
    torch.manual_seed(1001)
    model = PostaMulti(config)
    trainable_text_parameters(model)
    model = model.half().to(device) if device.type == 'cuda' else model.to(device)
    return model


def full_smoke(output, *, length=24, max_masked=1):
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA unavailable; full Run 001 smoke needs the local GPU')
    device = torch.device('cuda')
    torch.set_num_threads(2)
    config = replace(Config(), text_pooling='mean')
    model = build_model(config, device)
    from cpu_master_adam import CPUMasterAdamW
    optimizer = CPUMasterAdamW((p for p in model.parameters() if p.requires_grad),
                               lr=5e-5, loss_scale=16.0, max_grad_norm=1.0)
    torch.cuda.reset_peak_memory_stats()
    result = synthetic_masked_step(model, optimizer, device, length=length,
                                   max_masked=max_masked)
    report = {
        'format': 'posta-multi-235m-run001-smoke-v1',
        'run_id': RUN_ID,
        'architecture_params': parameter_counts(config)['total'],
        'trainable_text_params': sum(p.numel() for p in model.parameters() if p.requires_grad),
        'pooling': config.text_pooling,
        'checkpointing': True,
        'gpu_memory_fraction': 0.45,
        'sequence_length': length,
        'masked_positions': max_masked,
        'cuda_peak_allocated_mib': round(torch.cuda.max_memory_allocated() / 2**20, 1),
        'synthetic_training': result,
        'trained_on_real_data': False,
        'saved_weights': False,
    }
    from compare_checkpoints import atomic_report
    if Path(output).exists():
        raise FileExistsError('Run 001 smoke report already exists; refusing to overwrite')
    atomic_report(output, report)
    print(json.dumps(report, ensure_ascii=False), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True)
    parser.add_argument('--length', type=int, default=24)
    parser.add_argument('--max-masked', type=int, default=1)
    args = parser.parse_args()
    full_smoke(args.output, length=args.length, max_masked=args.max_masked)


if __name__ == '__main__':
    main()
