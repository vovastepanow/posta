"""Posta Multi 235M Run 001: resumable text pretraining from RANDOM weights.

The full 235,179,777-parameter model exists in memory; only the text tower
receives gradients in stage 1. On a GTX 1060 3 GiB use activation checkpointing,
FP16 forward, CPU-master FP32 AdamW, and a 45%-VRAM allocator ceiling.
This is not an invitation to use the quarantined corpus without rights review.

Run directory is new and separate from historical v1–v9 checkpoints.
"""
import argparse
import hashlib
import json
import math
import shutil
import time
from dataclasses import asdict, replace
from pathlib import Path

import torch
import torch.nn.functional as F
from tokenizers import Tokenizer

from cpu_master_adam import CPUMasterAdamW
from run001_continuation import resume_policy, extension_lr_multiplier
from run001_research_policy import require_local_research_quarantine
from corpus import Corpus
from model import PostaMulti
from run001_data import (POLICY, approval_issues, balanced_validation_windows,
                         make_windows, require_full_heldout_isolation,
                         require_isolated_splits, select_records)
from run001_smoke import RUN_ID, trainable_text_parameters
from spec import Config, parameter_counts
from train_text import save_atomic


def tokenizer_provenance(path):
    path = Path(path)
    contents = path.read_bytes()
    digest = hashlib.sha256(contents).hexdigest()
    tokenizer = Tokenizer.from_file(str(path))
    for text, value in (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2), ('[MASK]', 3)):
        if tokenizer.token_to_id(text) != value:
            raise ValueError('invalid tokenizer special token ID for ' + text)
    size = tokenizer.get_vocab_size()
    if not 260 <= size <= 65536:
        raise ValueError('tokenizer outside 235M supported vocabulary range')
    metadata = path.parent / 'metadata.json'
    if metadata.is_file():
        info = json.loads(metadata.read_text(encoding='utf-8'))
        if info.get('tokenizer_sha256') != digest:
            raise ValueError('tokenizer metadata integrity check failed')
    return tokenizer, digest, size


def masked_batch(window, generator, max_masked=8):
    """Create one reproducible masked-language-model example on CPU."""
    if len(window) < 4 or max_masked < 1 or window[0] != 1 or window[-1] != 2:
        raise ValueError('invalid tokenized window')
    ids = torch.tensor([window], dtype=torch.long)
    positions = torch.randperm(len(window)-2, generator=generator)[:min(
        max_masked, max(1, int(round(0.15 * (len(window)-2)))))] + 1
    selected = torch.zeros_like(ids, dtype=torch.bool)
    selected[0, positions] = True
    targets = ids[selected].clone()
    ids[selected] = 3
    return ids, selected, targets


def mlm_step_loss(model, window, generator, device, max_masked=8):
    inputs, selected, targets = masked_batch(window, generator, max_masked)
    inputs, selected, targets = (x.to(device) for x in (inputs, selected, targets))
    mask = torch.ones_like(inputs, dtype=torch.bool)
    logits = model.masked_token_logits(inputs, mask, selected).float()
    return F.cross_entropy(logits, targets)


@torch.no_grad()
def validation_loss(model, windows, device, max_examples=16, max_masked=8):
    if not windows:
        raise ValueError('no validation windows')
    was_training = model.training
    model.eval()
    generator = torch.Generator(device='cpu').manual_seed(23456)
    values = []
    try:
        for window in windows[:max_examples]:
            score = mlm_step_loss(model, window, generator, device, max_masked)
            if not math.isfinite(float(score)):
                raise RuntimeError('non-finite validation loss')
            values.append(float(score))
    finally:
        model.train(was_training)
    return round(sum(values)/len(values), 6)



@torch.no_grad()
def stratified_validation(model, windows, labels, device, max_masked=8):
    """Validation on all selected windows, macro-average over input groups."""
    if len(windows) != len(labels) or not windows:
        raise ValueError('validation labels and windows must align')
    generator = torch.Generator(device='cpu').manual_seed(23456)
    previous = model.training
    model.eval()
    groups = {}
    try:
        for window, label in zip(windows, labels):
            loss = float(mlm_step_loss(model, window, generator, device, max_masked))
            if not math.isfinite(loss):
                raise RuntimeError('nonfinite validation loss')
            groups.setdefault(label, []).append(loss)
    finally:
        model.train(previous)
    result = {name: {'windows': len(values),
                     'mean_loss': round(sum(values)/len(values), 6)}
              for name, values in sorted(groups.items())}
    macro = sum(info['mean_loss'] for info in result.values()) / len(result)
    return {'macro_mean_loss': round(macro, 6), 'groups': result,
            'evaluated_windows': len(windows)}


def schedule(step, total, base_lr, warmup):
    if not 1 <= step <= total or not 0 < base_lr <= 0.01 or        warmup < 0 or warmup >= total:
        raise ValueError('invalid schedule')
    if warmup and step <= warmup:
        return base_lr * step / warmup
    progress = (step - warmup) / max(1, total - warmup)
    return base_lr * (0.1 + 0.9 * 0.5 * (1 + math.cos(math.pi * progress)))


def readiness(dataset, tokenizer_file):
    corpus = Corpus(dataset)
    issues = approval_issues(corpus.manifest)
    tok, digest, size = tokenizer_provenance(tokenizer_file)
    report = {'format': 'posta-multi-run001-readiness-v1',
              'run_id': RUN_ID,
              'manifest_sha256': corpus.manifest_hash,
              'tokenizer_sha256': digest,
              'tokenizer_vocab_size': size,
              'model_parameters': parameter_counts(replace(Config(), text_pooling='mean'))['total'],
              'dataset_approval_issues': issues,
              'cuda_available': bool(torch.cuda.is_available()),
              'allowed_to_train': not issues and bool(torch.cuda.is_available()),
              'note': 'approval is documentary, not evidence of quality or topic coverage'}
    return report


def run(args):
    if getattr(args, 'extend_steps', False) and not args.resume:
        raise ValueError('--extend-steps requires --resume')
    if getattr(args, 'reconfigure_extension', False) and not getattr(args, 'extend_steps', False):
        raise ValueError('--reconfigure-extension requires --extend-steps')
    if getattr(args, 'extension_lr_multiplier', None) is not None and not getattr(args, 'reconfigure_extension', False):
        raise ValueError('--extension-lr-multiplier requires --reconfigure-extension')
    if getattr(args, 'reconfigure_extension', False) and (
            args.extension_lr_multiplier is None or
            not math.isfinite(args.extension_lr_multiplier) or
            not 0 < args.extension_lr_multiplier <= 1):
        raise ValueError('reconfigured extension needs a finite LR multiplier in (0, 1]')
    if getattr(args, 'expand_train_sample', False) and not getattr(args, 'reconfigure_extension', False):
        raise ValueError('--expand-train-sample requires --reconfigure-extension')
    if args.steps < 1 or args.save_every < 1 or args.validate_every < 1 or        args.grad_accum < 1 or args.max_masked < 1 or args.patience < 1 or        args.train_per_group < 1 or args.val_per_group < 1 or args.val_windows_per_group < 1 or        args.seq_length < 8 or args.seq_length > 512 or        args.warmup < 0 or args.warmup >= args.steps or        not 0 < args.lr <= 0.01:
        raise ValueError('invalid Run 001 training settings')

    corpus = Corpus(args.dataset)
    issues = approval_issues(corpus.manifest)
    local_research = getattr(args, 'local_research_quarantine', False)
    if local_research:
        require_local_research_quarantine(corpus)
    elif issues:
        raise ValueError('dataset is not approved for training: ' + ', '.join(issues))
    if not torch.cuda.is_available():
        raise RuntimeError('Run 001 full 235M training needs CUDA')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    tokenizer, tokenizer_sha, vocab = tokenizer_provenance(args.tokenizer)
    meta_path = Path(args.tokenizer).parent / 'metadata.json'
    if local_research:
        if not meta_path.is_file():
            raise ValueError('local research requires its own tokenizer provenance')
        tokenizer_meta = json.loads(meta_path.read_text(encoding='utf-8'))
        if (tokenizer_meta.get('quarantined_research_only') is not True or
                tokenizer_meta.get('manifest_sha256') != corpus.manifest_hash):
            raise ValueError('research tokenizer must be fitted on exactly this quarantined corpus')
    elif meta_path.is_file():
        tokenizer_meta = json.loads(meta_path.read_text(encoding='utf-8'))
        if tokenizer_meta.get('quarantined_research_only'):
            raise ValueError('quarantined tokenizer cannot be used in approved model training')
    config = replace(Config(), text_pooling='mean')
    # Keep the full 235M architecture; unused vocabulary slots are permitted.
    if vocab > config.vocab_size:
        raise ValueError('tokenizer exceeds 235M vocabulary')
    selected_train, train_counts = select_records(
        corpus, split='train', max_per_group=args.train_per_group)
    selected_val, val_counts = select_records(
        corpus, split='validation', max_per_group=args.val_per_group)
    require_isolated_splits(selected_train, selected_val)
    if getattr(args, 'expand_train_sample', False):
        require_full_heldout_isolation(corpus, selected_train)
    train, train_sha, train_windows = make_windows(
        selected_train, tokenizer, args.seq_length)
    val, val_labels, val_sha, val_windows = balanced_validation_windows(
        selected_val, tokenizer, args.seq_length,
        per_group=args.val_windows_per_group)
    if len(train) < 128 or len(val) < 16:
        raise ValueError('too few text windows for a 235M pretraining experiment')
    output = Path(args.output)
    last = output / 'run001-text-last.pt'
    best = output / 'run001-text-best.pt'
    if not args.resume and (last.exists() or best.exists()):
        raise FileExistsError('Run 001 directory already contains checkpoints')
    if args.resume and not last.is_file():
        raise FileNotFoundError('resume checkpoint is missing')
    output.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(output).free < 8 * 1024**3:
        raise RuntimeError('need at least 8 GiB of free HDD space for atomic checkpoint writes')

    settings = {
        'format': 'posta-multi-235m-run001-text-v3',
        'run_id': RUN_ID,
        'quarantined_research_only': local_research,
        'private_local_only': local_research,
        'production_promotion_allowed': False,
        'config': asdict(config),
        'manifest_sha256': corpus.manifest_hash,
        'tokenizer_sha256': tokenizer_sha,
        'train_selection_sha256': train_sha,
        'val_selection_sha256': val_sha,
        'sample_policy': POLICY,
        'steps': args.steps,
        'grad_accum': args.grad_accum,
        'max_masked': args.max_masked,
        'lr': args.lr, 'warmup': args.warmup,
        'seed': args.seed,
        'max_grad_norm': 1.0,
        'train_per_group': args.train_per_group,
        'val_per_group': args.val_per_group,
        'val_windows_per_group': args.val_windows_per_group,
        'validation_selection': 'stratified-hash-kind-language-v1',
        'train_validation_isolation': 'cross-kind-nfkc-casefold-exact-v1',
        'validation_metric': 'macro_mean_loss_across_groups',
        'seq_length': args.seq_length,
        'validate_every': args.validate_every,
        'patience': args.patience,
    }
    torch.manual_seed(args.seed)
    torch.cuda.manual_seed_all(args.seed)
    model = PostaMulti(config).half().cuda()
    model.enable_gradient_checkpointing()
    parameters = trainable_text_parameters(model)
    optimizer = CPUMasterAdamW(parameters, lr=args.lr, loss_scale=16.0,
                               max_grad_norm=1.0)
    generator = torch.Generator(device='cpu').manual_seed(args.seed + 7)
    start, best_loss, best_step, stale = 0, math.inf, 0, 0
    continuation_phase = None
    if args.resume:
        state = torch.load(last, weights_only=True, map_location='cpu')
        if state.get('format') != settings['format']:
            raise ValueError('Run 001 text checkpoint format mismatch')
        continuation_phase, extended = resume_policy(
            state, settings, extend=getattr(args, 'extend_steps', False),
            reconfigure=getattr(args, 'reconfigure_extension', False),
            start_lr_multiplier=getattr(args, 'extension_lr_multiplier', None),
            expand_train_sample=getattr(args, 'expand_train_sample', False))
        model.load_state_dict(state['model'])
        optimizer.load_state_dict(state['optimizer'])
        torch.set_rng_state(state['cpu_rng'])
        torch.cuda.set_rng_state_all(state['cuda_rng'])
        generator.set_state(state['sampler_rng'])
        start, best_loss, best_step, stale = (
            state['step'], state['best_loss'], state['best_step'], state['stale'])
        if extended:
            stale = 0  # New explicit phase; keep best score and best checkpoint.
        del state

    print(json.dumps({
        'run_id': RUN_ID, 'stage': 'text-mlm',
        'scratch_initialized': start == 0,
        'parameter_count': sum(p.numel() for p in model.parameters()),
        'text_trainable': sum(p.numel() for p in parameters),
        'train_windows': len(train), 'val_windows': len(val),
        'train_groups': train_counts, 'val_groups': val_counts,
        'val_windows_by_group': val_windows,
        'gpu_fraction': 0.45,
        'quarantined_research_only': local_research,
        'production_promotion_allowed': False,
        'continuation_phase': continuation_phase,
    }, ensure_ascii=False), flush=True)

    device = torch.device('cuda')
    torch.cuda.reset_peak_memory_stats(device)
    for step in range(start+1, args.steps+1):
        step_started = time.monotonic()
        model.train()
        optimizer.lr = (args.lr * extension_lr_multiplier(step, continuation_phase)
                        if continuation_phase else
                        schedule(step, args.steps, args.lr, args.warmup))
        optimizer.zero_grad()
        losses = []
        input_tokens = 0
        for micro in range(args.grad_accum):
            sample = train[int(torch.randint(len(train), (1,), generator=generator).item())]
            input_tokens += len(sample)
            value = mlm_step_loss(model, sample, generator, device, args.max_masked)
            if not bool(torch.isfinite(value)):
                raise RuntimeError('non-finite MLM loss; previous checkpoint preserved')
            optimizer.backward(value / args.grad_accum)
            losses.append(float(value.detach().cpu()))
        if not optimizer.step():
            raise RuntimeError('non-finite gradients; previous checkpoint preserved')

        check = step % args.validate_every == 0 or step == args.steps
        current_val = None
        current_groups = None
        if check:
            metrics = stratified_validation(model, val, val_labels, device,
                                            max_masked=args.max_masked)
            score = metrics['macro_mean_loss']
            current_val = score
            current_groups = metrics['groups']
            if score + 0.0001 < best_loss:
                best_loss, best_step, stale = score, step, 0
                save_atomic(best, {
                    'format': settings['format'] + '-best',
                    'settings': settings, 'step': step,
                    'validation_loss': score, 'validation': metrics,
                    'continuation_phase': continuation_phase,
                    'model': model.state_dict()})
            else:
                stale += 1
        if check or step % args.save_every == 0:
            save_atomic(last, {
                'format': settings['format'], 'settings': settings, 'step': step,
                'model': model.state_dict(), 'optimizer': optimizer.state_dict(),
                'cpu_rng': torch.get_rng_state(),
                'cuda_rng': torch.cuda.get_rng_state_all(),
                'continuation_phase': continuation_phase,
                'sampler_rng': generator.get_state(),
                'best_loss': best_loss, 'best_step': best_step, 'stale': stale,
            })
            print(json.dumps({'stage': 'text-mlm', 'step': step,
                              'train_loss': round(sum(losses)/len(losses), 6),
                              'step_elapsed_seconds': round(time.monotonic() - step_started, 3),
                              'input_tokens': input_tokens,
                              'cuda_peak_allocated_mib': round(torch.cuda.max_memory_allocated(device) / 1024**2, 1),
                              'gradient_global_norm': (round(optimizer.last_grad_norm, 6)
                                      if optimizer.last_grad_norm is not None else None),
                              'gradient_clip_multiplier': round(optimizer.last_grad_multiplier, 6),
                              'val_loss': current_val,
                              'val_group_losses': current_groups,
                              'best_val_loss': best_loss if check else None,
                              'best_step': best_step, 'stale': stale},
                             ensure_ascii=False), flush=True)
        if check and stale >= args.patience:
            print(json.dumps({'early_stopped': True, 'step': step,
                              'best_step': best_step}), flush=True)
            break
    return best


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--output', help='new Run 001 checkpoint directory (not used by --preflight)')
    p.add_argument('--preflight', action='store_true',
                   help='read-only provenance check; does not instantiate the network')
    p.add_argument('--local-research-quarantine', action='store_true',
                   help='private research on screened local quarantine; weights can never be promoted')
    p.add_argument('--resume', action='store_true')
    p.add_argument('--extend-steps', action='store_true',
                   help='after completing previous budget, extend on exactly the same corpus and tokenizer at lower LR')
    p.add_argument('--reconfigure-extension', action='store_true',
                   help='at a completed phase boundary, explicitly change accumulation or validation cadence')
    p.add_argument('--extension-lr-multiplier', type=float,
                   help='explicit new phase starting LR as a fraction of the original base LR, at most 1')
    p.add_argument('--expand-train-sample', action='store_true',
                   help='at a completed phase boundary, explicitly increase the same-corpus train sample cap')
    p.add_argument('--steps', type=int, default=2000)
    p.add_argument('--save-every', type=int, default=100)
    p.add_argument('--validate-every', type=int, default=100)
    p.add_argument('--grad-accum', type=int, default=2)
    p.add_argument('--max-masked', type=int, default=8)
    p.add_argument('--patience', type=int, default=8)
    p.add_argument('--train-per-group', type=int, default=2000)
    p.add_argument('--val-per-group', type=int, default=64)
    p.add_argument('--val-windows-per-group', type=int, default=4,
                   help='stable validation windows per language/modality group')
    p.add_argument('--seq-length', type=int, default=96)
    p.add_argument('--lr', type=float, default=0.0001)
    p.add_argument('--warmup', type=int, default=100)
    p.add_argument('--seed', type=int, default=1001)
    args = p.parse_args()
    if args.preflight:
        print(json.dumps(readiness(args.dataset, args.tokenizer), ensure_ascii=False), flush=True)
        return
    if not args.output:
        p.error('--output is required unless --preflight is supplied')
    best = run(args)
    print(json.dumps({'run_id': RUN_ID, 'best_checkpoint': str(best)}), flush=True)


if __name__ == '__main__':
    main()
