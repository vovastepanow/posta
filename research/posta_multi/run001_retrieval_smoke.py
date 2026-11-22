"""Run 001 retrieval: one synthetic full-235M anti-collapse GPU optimizer step.

Only deterministic generated token IDs. No corpus, old checkpoint, uploads or
training-data approval bypass. This checks execution, NOT search quality.
"""
import argparse
import json
import math
from dataclasses import replace
from pathlib import Path

import torch

from compare_checkpoints import atomic_report
from cpu_master_adam import CPUMasterAdamW
from model import PostaMulti
from run001_retrieval import (cached_retrieval_backward,
                              configure_trainable_stage)
from run001_smoke import RUN_ID, trainable_text_parameters
from spec import Config, parameter_counts


def synthetic_triples(vocab_size, length=96, samples=3, seed=1762):
    if vocab_size < 200 or length < 8 or samples < 2:
        raise ValueError('invalid synthetic retrieval budget')
    random = torch.Generator(device='cpu').manual_seed(seed)
    pairs = []
    for _ in range(samples):
        q = [1, *torch.randint(4, vocab_size, (length-2,),
                                generator=random).tolist(), 2]
        positive = q.copy()
        positive[1] = (positive[1] + 1) % (vocab_size-4) + 4
        negative = [1, *torch.randint(4, vocab_size, (length-2,),
                                      generator=random).tolist(), 2]
        pairs.append((tuple(q), tuple(positive), tuple(negative)))
    return pairs


def run(output, *, length=96, samples=3):
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA required for the full-size GPU smoke')
    output = Path(output)
    if output.exists():
        raise FileExistsError('synthetic retrieval report already exists')
    device = torch.device('cuda')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    torch.manual_seed(1762)
    config = replace(Config(), text_pooling='mean-residual')
    if type(length) is not int or not 8 <= length <= config.text_length or type(samples) is not int or samples < 2:
        raise ValueError('invalid retrieval smoke dimensions')
    model = PostaMulti(config, dropout=0).half().cuda()
    model.enable_gradient_checkpointing(True)
    trainable = trainable_text_parameters(model)
    # Probe the FULL text-tower gradient phase, not just the cheap projection.
    phase = configure_trainable_stage(model, step=101, projection_only_steps=100)
    optimizer = CPUMasterAdamW(trainable, lr=0.00001,
                               loss_scale=16, max_grad_norm=1.0)
    pairs = synthetic_triples(config.vocab_size, length=length, samples=samples)
    torch.cuda.reset_peak_memory_stats()
    before = model.text_projection.weight.detach().float().cpu().clone()
    optimizer.zero_grad()
    loss, measures = cached_retrieval_backward(
        model, pairs, device, optimizer=optimizer)
    if not math.isfinite(loss) or optimizer.step() is not True:
        raise RuntimeError('synthetic retrieval gradient is invalid')
    updated = bool(torch.any(
        before != model.text_projection.weight.detach().float().cpu()))
    if not updated:
        raise RuntimeError('retrieval update did not change projection weights')
    report = {
        'format': 'posta-multi-235m-run001-retrieval-smoke-v1',
        'run_id': RUN_ID,
        'architecture_params': parameter_counts(config)['total'],
        'text_trainable_params': sum(p.numel() for p in trainable),
        'sequence_length': length,
        'explicit_triplets': len(pairs),
        'stage': phase,
        'loss': round(loss, 6),
        'training_geometry': {k: round(v, 6) for k, v in measures.items()},
        'global_gradient_norm': optimizer.last_grad_norm,
        'cuda_peak_allocated_mib': round(torch.cuda.max_memory_allocated()/2**20, 1),
        'updated_projection': updated,
        'trained_on_real_data': False,
        'checkpoint_saved': False,
        'search_quality_verified': False,
    }
    atomic_report(output, report)
    print(json.dumps(report, ensure_ascii=False), flush=True)
    return report


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--output', required=True)
    p.add_argument('--seq-length', type=int, default=96)
    p.add_argument('--samples', type=int, default=3)
    args = p.parse_args()
    run(args.output, length=args.seq_length, samples=args.samples)


if __name__ == '__main__':
    main()
