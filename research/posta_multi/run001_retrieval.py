"""Posta Multi 235M / Run 001: memory-bounded anti-collapse retrieval stage.

Stage 2 is MANUAL ONLY, after Run 001 text pretraining and corpus review.
Every query has one explicitly labeled positive and negative from a curated
split. Gradient caching recomputes one 96-token text at a time: the exact
batch objective still backpropagates into the shared 235M text tower, while
only one transformer computation graph occupies GPU memory at once.

No historical v1-v9 model is loaded; no production model is promoted.
"""
import argparse
import hashlib
import json
import math
import shutil
from dataclasses import asdict, replace
from pathlib import Path

import torch
from tokenizers import Tokenizer

from corpus import Corpus
from cpu_master_adam import CPUMasterAdamW
from model import PostaMulti
from ranking_metrics import conservative_rank, summarize_ranks
from retrieval_data_quality import (RUN001_CURATED_POLICY,
                                    curate_retrieval_pairs)
from run001_data import approval_issues
from run001_retrieval_loss import anti_collapse_loss, checkpoint_is_healthy
from run001_smoke import RUN_ID, trainable_text_parameters
from train_text import save_atomic


STAGE_FORMAT = 'posta-multi-235m-run001-retrieval-v1'


def configure_trainable_stage(model, step, projection_only_steps):
    """Protect MLM representations while the random retrieval head warms up."""
    if step < 1 or projection_only_steps < 0:
        raise ValueError('invalid projection-only schedule')
    projection_only = step <= projection_only_steps
    text_prefixes = ('text_tokens.', 'text_positions.', 'text_encoder.',
                     'text_norm.', 'text_projection.')
    for name, param in model.named_parameters():
        is_text = name.startswith(text_prefixes)
        is_projection = name.startswith('text_projection.')
        param.requires_grad_(is_projection if projection_only else is_text)
    return 'projection-only' if projection_only else 'text-finetune'



def encode_triples(tokenizer, pairs, max_length=96):
    if max_length < 8 or max_length > 512:
        raise ValueError('retrieval token budget outside Run 001 range')
    tokenizer.enable_truncation(max_length=max_length)
    converted = []
    for triple in pairs:
        encoded = []
        for content in triple:
            ids = tuple(tokenizer.encode(content).ids)
            if len(ids) < 3 or ids[0] != 1 or ids[-1] != 2:
                raise ValueError('tokenized text must have CLS/content/SEP')
            encoded.append(ids)
        converted.append(tuple(encoded))
    return converted


def _encode_one(model, token_ids, device):
    inputs = torch.tensor([token_ids], device=device, dtype=torch.long)
    mask = torch.ones_like(inputs, dtype=torch.bool)
    return model.encode_text(inputs, mask).squeeze(0)


def cached_retrieval_backward(model, encoded, device, optimizer=None,
                              loss_scale=1.0):
    """Exact re-forward embedding-gradient cache with deterministic dropout=0.

    If optimizer supplied, use its FP16 loss scaling; otherwise normal
    PyTorch backward. Do not call optimizer.step here. The same model weights
    and deterministic dropout setting must be used in both passes.
    """
    if len(encoded) < 2 or not all(len(triple) == 3 for triple in encoded):
        raise ValueError('need >=2 explicit-negative retrieval triples')
    if not math.isfinite(loss_scale) or loss_scale <= 0:
        raise ValueError('invalid loss scale')
    model.eval()
    with torch.no_grad():
        vectors = [torch.stack([_encode_one(model, pair[role], device)
                                for pair in encoded]).float()
                   for role in (0, 1, 2)]
    leaves = [vector.detach().requires_grad_(True) for vector in vectors]
    total, metrics = anti_collapse_loss(*leaves)
    grads = torch.autograd.grad(total, leaves, create_graph=False)
    # Keeping dropout disabled makes the recomputation exactly deterministic
    # while model.train() enables text activation checkpointing.
    model.train()
    for role in (0, 1, 2):
        for i, triple in enumerate(encoded):
            output = _encode_one(model, triple[role], device)
            proxy = (output.float() * grads[role][i].detach()).sum()
            if optimizer is not None:
                optimizer.backward(proxy)
            else:
                (proxy * loss_scale).backward()
    return float(total.detach()), metrics


@torch.no_grad()
def evaluate_retrieval(model, triples, device):
    """Explicit-negative local ranking + cosine geometry, no GPU training."""
    if len(triples) < 3:
        raise ValueError('evaluation needs at least three curated queries')
    was_training = model.training
    model.eval()
    try:
        queries = list(dict.fromkeys(triple[0] for triple in triples))
        documents = list(dict.fromkeys(
            text for _, p, n in triples for text in (p, n)))
        qv = torch.stack([_encode_one(model, text, device).float().cpu()
                          for text in queries])
        dv = torch.stack([_encode_one(model, text, device).float().cpu()
                          for text in documents])
    finally:
        model.train(was_training)

    q_idx = {value: i for i, value in enumerate(queries)}
    d_idx = {value: i for i, value in enumerate(documents)}
    scores = qv @ dv.T
    ranks = []
    margins = []
    for q, p, n in triples:
        values = scores[q_idx[q]]
        positive_score = float(values[d_idx[p]])
        negative_score = float(values[d_idx[n]])
        margins.append(positive_score - negative_score)
        ranks.append(conservative_rank(values.tolist(), d_idx[p]))
    from retrieval_health import off_diagonal_cosine
    return {
        'samples': len(triples),
        'candidate_pool_size': len(documents),
        'pair_accuracy': round(sum(x > 0 for x in margins)/len(margins), 6),
        'mean_positive_minus_negative': round(sum(margins)/len(margins), 6),
        'query_interexample_cosine': off_diagonal_cosine(qv),
        'document_interexample_cosine': off_diagonal_cosine(dv),
        **summarize_ranks(ranks),
    }


def better_candidate(candidate, best):
    """Only store a healthy candidate that improves MRR without pair decay."""
    if not checkpoint_is_healthy(candidate):
        return False
    if best is None:
        return True
    return (candidate['mean_reciprocal_rank'] > best['mean_reciprocal_rank'] + 0.001
            and candidate['pair_accuracy'] >= best['pair_accuracy'] - 0.01
            and candidate['query_interexample_cosine'] <= 0.97
            and candidate['document_interexample_cosine'] <= 0.97)


def load_stage1_source(path, corpus, tokenizer_sha):
    with Path(path).open('rb') as f:
        source_sha = hashlib.file_digest(f, 'sha256').hexdigest()
    source = torch.load(path, map_location='cpu', weights_only=True)
    if source.get('format') != 'posta-multi-235m-run001-text-v3-best':
        raise ValueError('stage 2 requires completed Run 001 v3 text-best checkpoint')
    settings = source.get('settings', {})
    if (settings.get('run_id') != RUN_ID or
            settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('tokenizer_sha256') != tokenizer_sha):
        raise ValueError('stage 1 checkpoint dataset/tokenizer provenance mismatch')
    from spec import Config, parameter_counts
    config = Config(**settings['config'])
    if (config.text_pooling != 'mean' or
            parameter_counts(config)['total'] != 235179777):
        raise ValueError('stage 1 is not the selected 235M architecture')
    return source, config, source_sha


def run(args):
    if (args.steps < 1 or args.batch_size < 2 or args.train_pairs < args.batch_size
            or args.val_pairs < 16 or args.validate_every < 1
            or args.save_every < 1 or args.patience < 1 or
            not 0 < args.lr <= 0.001 or args.projection_only_steps < 0 or
            args.projection_only_steps >= args.steps or args.max_length < 8
            or args.max_length > 512):
        raise ValueError('invalid Run 001 retrieval settings')
    corpus = Corpus(args.dataset)
    issues = approval_issues(corpus.manifest)
    if issues:
        raise ValueError('dataset not approved for training: ' + ', '.join(issues))
    if not torch.cuda.is_available():
        raise RuntimeError('Run 001 retrieval stage requires CUDA')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)

    tok_path = Path(args.tokenizer)
    tok_sha = hashlib.sha256(tok_path.read_bytes()).hexdigest()
    tokenizer = Tokenizer.from_file(str(tok_path))
    for special, expected in (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2)):
        if tokenizer.token_to_id(special) != expected:
            raise ValueError('tokenizer special IDs do not match Run 001')
    source, config, source_sha = load_stage1_source(
        args.init, corpus, tok_sha)
    # Stage 1 MLM only uses _text_hidden: its 'mean' retrieval pool is
    # interchangeable with this bypass without changing any weights.
    config = replace(config, text_pooling='mean-residual')

    training, train_sha, train_stats = curate_retrieval_pairs(
        corpus, args.train_pairs, split='train', avoid_heldout=True,
        strict_heldout_negatives=True)
    validation, val_sha, _ = curate_retrieval_pairs(
        corpus, args.val_pairs, split='validation', avoid_heldout=False)
    train = encode_triples(tokenizer, training, args.max_length)
    val = encode_triples(tokenizer, validation, args.max_length)
    out = Path(args.output)
    last = out / 'run001-retrieval-last.pt'
    best = out / 'run001-retrieval-best.pt'
    if not args.resume and (last.exists() or best.exists()):
        raise FileExistsError('Run 001 retrieval checkpoint exists')
    if args.resume and not last.exists():
        raise FileNotFoundError('retrieval resume checkpoint missing')
    out.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(out).free < 8*1024**3:
        raise RuntimeError('need >=8 GiB free for atomic 235M checkpoints')
    settings = {
        'format': STAGE_FORMAT, 'run_id': RUN_ID,
        'config': asdict(config), 'init_sha256': source_sha,
        'manifest_sha256': corpus.manifest_hash,
        'tokenizer_sha256': tok_sha,
        'curation_policy': RUN001_CURATED_POLICY,
        'train_pairs_sha256': train_sha,
        'val_pairs_sha256': val_sha,
        'train_pairs': args.train_pairs, 'val_pairs': args.val_pairs,
        'batch_size': args.batch_size,
        'max_length': args.max_length,
        'steps': args.steps, 'learning_rate': args.lr, 'seed': args.seed,
        'validate_every': args.validate_every,
        'save_every': args.save_every,
        'patience': args.patience,
        'objective': 'explicit-margin-variance-repulsion-v1',
        'gradient_cache': 'recompute-one-text-at-a-time-dropout-zero-v1',
        'max_grad_norm': 1.0,
        'projection_only_steps': args.projection_only_steps,
        'finetune_learning_rate_multiplier': 0.25,
    }
    torch.manual_seed(args.seed)
    torch.cuda.manual_seed_all(args.seed)
    model = PostaMulti(config, dropout=0).half().cuda()
    model.load_state_dict(source['model'])
    model.enable_gradient_checkpointing(True)
    del source
    trainable = trainable_text_parameters(model)
    optimizer = CPUMasterAdamW(trainable, lr=args.lr, loss_scale=16,
                               max_grad_norm=1.0)
    generator = torch.Generator(device='cpu').manual_seed(args.seed + 99)
    step, best_metrics, best_step, stale = 0, None, 0, 0
    if args.resume:
        checkpoint = torch.load(last, weights_only=True, map_location='cpu')
        if checkpoint.get('format') != STAGE_FORMAT or checkpoint.get('settings') != settings:
            raise ValueError('retrieval resume provenance/settings mismatch')
        model.load_state_dict(checkpoint['model'])
        optimizer.load_state_dict(checkpoint['optimizer'])
        torch.set_rng_state(checkpoint['cpu_rng'])
        torch.cuda.set_rng_state_all(checkpoint['cuda_rng'])
        generator.set_state(checkpoint['sampler_rng'])
        step, best_metrics, best_step, stale = (
            checkpoint['step'], checkpoint['best_metrics'],
            checkpoint['best_step'], checkpoint['stale'])
        if step >= args.steps:
            raise ValueError('already completed all requested steps')

    print(json.dumps({
        'run_id': RUN_ID, 'stage': 'retrieval-anti-collapse',
        'architecture_params': sum(p.numel() for p in model.parameters()),
        'train_samples': len(train), 'validation_samples': len(val),
        'curation_counts': train_stats, 'gpu_allocator_fraction': 0.45,
    }), flush=True)
    device = torch.device('cuda')
    for step in range(step + 1, args.steps + 1):
        chosen = torch.randperm(len(train), generator=generator)[:args.batch_size]
        batch = [train[int(i)] for i in chosen]
        phase = configure_trainable_stage(model, step, args.projection_only_steps)
        optimizer.lr = (args.lr if phase == 'projection-only'
                        else args.lr * 0.25)
        optimizer.zero_grad()
        loss, measures = cached_retrieval_backward(model, batch, device,
                                                    optimizer=optimizer)
        if not optimizer.step():
            raise RuntimeError('nonfinite retrieval gradient; last checkpoint preserved')
        check = step % args.validate_every == 0 or step == args.steps
        val_metrics = None
        if check:
            val_metrics = evaluate_retrieval(model, val, device)
            if better_candidate(val_metrics, best_metrics):
                best_metrics, best_step, stale = val_metrics, step, 0
                save_atomic(best, {
                    'format': STAGE_FORMAT + '-best', 'settings': settings,
                    'step': step, 'metrics': val_metrics,
                    'model': model.state_dict(),
                })
            else:
                stale += 1
        if check or step % args.save_every == 0:
            save_atomic(last, {
                'format': STAGE_FORMAT, 'settings': settings,
                'step': step, 'model': model.state_dict(),
                'optimizer': optimizer.state_dict(),
                'sampler_rng': generator.get_state(),
                'cpu_rng': torch.get_rng_state(),
                'cuda_rng': torch.cuda.get_rng_state_all(),
                'best_metrics': best_metrics, 'best_step': best_step,
                'stale': stale,
            })
            print(json.dumps({
                'stage': 'retrieval-anti-collapse', 'step': step,
                'train_objective': round(loss, 6),
                'phase': phase,
                'learning_rate': optimizer.lr,
                'train_geometry': {k: round(v, 6) for k, v in measures.items()},
                'val_metrics': val_metrics,
                'healthy': checkpoint_is_healthy(val_metrics),
                'best_step': best_step,
                'gradient_norm': optimizer.last_grad_norm,
            }), flush=True)
        if check and stale >= args.patience:
            print(json.dumps({'stopped_early': True, 'step': step}), flush=True)
            break
    return best if best.exists() else None


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--init', required=True, help='run001-text-best.pt only')
    p.add_argument('--output', required=True)
    p.add_argument('--steps', type=int, default=500)
    p.add_argument('--batch-size', type=int, default=4)
    p.add_argument('--train-pairs', type=int, default=512)
    p.add_argument('--val-pairs', type=int, default=64)
    p.add_argument('--max-length', type=int, default=96)
    p.add_argument('--validate-every', type=int, default=25)
    p.add_argument('--save-every', type=int, default=25)
    p.add_argument('--patience', type=int, default=8)
    p.add_argument('--lr', type=float, default=0.00001)
    p.add_argument('--projection-only-steps', type=int, default=100)
    p.add_argument('--seed', type=int, default=1001)
    p.add_argument('--resume', action='store_true')
    args = p.parse_args()
    result = run(args)
    print(json.dumps({
        'run_id': RUN_ID, 'stage': 'retrieval-anti-collapse',
        'best_checkpoint': str(result) if result else None,
        'promoted_to_production': False,
    }), flush=True)


if __name__ == '__main__':
    main()
