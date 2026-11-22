"""Posta Multi 235M / Run 001: memory-bounded anti-collapse retrieval stage.

Stage 2 is MANUAL ONLY. Approved training requires reviewed data. An explicit
quarantine flag allows private research with matching restricted stage 1
weights and tokenizer; it never grants production or release approval.
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
import time
from dataclasses import asdict, replace
from pathlib import Path

import torch
from tokenizers import Tokenizer

from corpus import Corpus
from run001_continuation import resume_policy, extension_lr_multiplier
from run001_research_policy import (ensure_no_unapproved_weights_in_stage2,
                                    require_local_research_quarantine)
from cpu_master_adam import CPUMasterAdamW
from model import PostaMulti
from ranking_metrics import conservative_rank, summarize_ranks
from retrieval_data_quality import (RUN001_CURATED_POLICY, POSITIVE_ONLY_POLICY,
                                    VIDEO_CAPTION_POLICY, curate_retrieval_pairs,
                                    curate_positive_only_pairs, curate_video_caption_pairs)
from run001_data import approval_issues
from run001_retrieval_loss import (anti_collapse_loss, checkpoint_is_healthy,
                                   positive_only_alignment_loss)
from run001_smoke import RUN_ID, trainable_text_parameters
from train_text import save_atomic


STAGE_FORMAT = 'posta-multi-235m-run001-retrieval-v1'
SELECTION_POLICY = 'healthy-max-mrr-v2'


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


def encode_positive_pairs(tokenizer, pairs, max_length=96):
    """Tokenize only known query/positive labels; no invented negatives."""
    tokenizer.enable_truncation(max_length=max_length)
    result = []
    for query, positive in pairs:
        inputs = (tuple(tokenizer.encode(query).ids),
                  tuple(tokenizer.encode(positive).ids))
        if any(len(ids) < 3 or ids[0] != 1 or ids[-1] != 2 for ids in inputs):
            raise ValueError('positive-only tokens need CLS/content/SEP')
        result.append(inputs)
    return result


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


def cached_positive_only_backward(model, encoded, device, optimizer=None,
                                  weight=0.25):
    """Gradient-cached positive-only alignment without labeling any negatives."""
    if len(encoded) < 2 or not all(len(row) == 2 for row in encoded):
        raise ValueError('need at least two labeled query/positive pairs')
    if not isinstance(weight, (int, float)) or not math.isfinite(weight) or weight <= 0:
        raise ValueError('positive-only loss weight must be finite and positive')
    model.eval()
    with torch.no_grad():
        vectors = [torch.stack([
            _encode_one(model, row[role], device) for row in encoded]).float()
            for role in (0, 1)]
    leaves = [v.detach().requires_grad_(True) for v in vectors]
    objective, metrics = positive_only_alignment_loss(*leaves)
    grads = torch.autograd.grad(objective * weight, leaves, create_graph=False)
    model.train()
    for role in (0, 1):
        for idx, row in enumerate(encoded):
            output = _encode_one(model, row[role], device)
            surrogate = (output.float() * grads[role][idx].detach()).sum()
            if optimizer is not None:
                optimizer.backward(surrogate)
            else:
                surrogate.backward()
    return float(objective.detach()), metrics


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
    """Store the best MRR subject to absolute retrieval-health gates."""
    if not checkpoint_is_healthy(candidate):
        return False
    if best is None:
        return True
    samples = candidate.get('samples')
    if best.get('samples') != samples:
        return False
    return (candidate['mean_reciprocal_rank'] > best['mean_reciprocal_rank'] + 0.001
            and candidate['query_interexample_cosine'] <= 0.97
            and candidate['document_interexample_cosine'] <= 0.97)


def resume_staleness(stale, patience, step, budget, *, reset=False):
    """Allow an explicit retry after early stopping without resetting weights."""
    if any(type(value) is not int for value in (stale, patience, step, budget)) or \
            stale < 0 or patience < 1 or not 0 <= step <= budget:
        raise ValueError('invalid early-stop checkpoint state')
    if reset:
        if stale < patience or step >= budget:
            raise ValueError('early-stop retry requires an unfinished early-stopped checkpoint')
        return 0
    return stale


def load_stage1_source(path, corpus, tokenizer_sha, *, local_research=False):
    with Path(path).open('rb') as f:
        source_sha = hashlib.file_digest(f, 'sha256').hexdigest()
        # Hash and load the same open inode even if an atomic best-checkpoint
        # replacement happens between those operations.
        f.seek(0)
        source = torch.load(f, map_location='cpu', weights_only=True)
    if source.get('format') != 'posta-multi-235m-run001-text-v3-best':
        raise ValueError('stage 2 requires completed Run 001 v3 text-best checkpoint')
    settings = source.get('settings', {})
    ensure_no_unapproved_weights_in_stage2(settings, local_research=local_research)
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
    video_cap = getattr(args, 'video_caption_pairs', 0)
    video_batch = getattr(args, 'video_caption_batch', 2)
    video_every = getattr(args, 'video_caption_every', 8)
    video_weight = getattr(args, 'video_caption_weight', 0.1)
    if getattr(args, 'resume_after_early_stop', False) and (
            not args.resume or getattr(args, 'extend_steps', False)):
        raise ValueError('--resume-after-early-stop requires ordinary --resume')
    if getattr(args, 'extend_steps', False) and not args.resume:
        raise ValueError('--extend-steps requires --resume')
    if (args.steps < 1 or args.batch_size < 2 or args.train_pairs < args.batch_size
            or args.val_pairs < 16 or args.validate_every < 1
            or args.save_every < 1 or args.patience < 1 or
            args.positive_only_pairs < 0 or args.positive_only_batch < 2 or
            args.positive_only_every < 1 or not 0 < args.positive_only_weight <= 1 or
            not 0 < args.lr <= 0.001 or args.projection_only_steps < 0 or
            args.projection_only_steps >= args.steps or args.max_length < 8
            or args.max_length > 512 or type(video_cap) is not int or video_cap < 0
            or type(video_batch) is not int or video_batch < 2
            or type(video_every) is not int or video_every < 1
            or type(video_weight) not in (int, float) or not math.isfinite(video_weight)
            or not 0 < video_weight <= 1):
        raise ValueError('invalid Run 001 retrieval settings')
    corpus = Corpus(args.dataset)
    issues = approval_issues(corpus.manifest)
    local_research = getattr(args, 'local_research_quarantine', False)
    if local_research:
        require_local_research_quarantine(corpus)
    elif issues:
        raise ValueError('dataset not approved for training: ' + ', '.join(issues))
    if video_cap and not local_research:
        raise ValueError('weak video-caption supervision requires explicit private research mode')
    if not torch.cuda.is_available():
        raise RuntimeError('Run 001 retrieval stage requires CUDA')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)

    tok_path = Path(args.tokenizer)
    tok_sha = hashlib.sha256(tok_path.read_bytes()).hexdigest()
    if local_research:
        meta_path = tok_path.parent / 'metadata.json'
        if not meta_path.is_file():
            raise ValueError('local research requires tokenizer provenance')
        metadata = json.loads(meta_path.read_text(encoding='utf-8'))
        if (metadata.get('quarantined_research_only') is not True or
                metadata.get('manifest_sha256') != corpus.manifest_hash or
                metadata.get('tokenizer_sha256') != tok_sha):
            raise ValueError('research tokenizer provenance mismatch')
    tokenizer = Tokenizer.from_file(str(tok_path))
    for special, expected in (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2)):
        if tokenizer.token_to_id(special) != expected:
            raise ValueError('tokenizer special IDs do not match Run 001')
    source, config, source_sha = load_stage1_source(
        args.init, corpus, tok_sha, local_research=local_research)
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
    extra_pairs, extra_sha, extra_stats = curate_positive_only_pairs(
        corpus, args.positive_only_pairs, forbidden_queries=(
            row[0] for row in training))
    extra = encode_positive_pairs(tokenizer, extra_pairs, args.max_length)
    extra_enabled = len(extra) >= args.positive_only_batch
    video, video_sha, video_stats = [], None, None
    if video_cap:
        video_pairs, video_sha, video_stats = curate_video_caption_pairs(corpus, video_cap)
        video = encode_positive_pairs(tokenizer, video_pairs, args.max_length)
        if len(video) < video_batch:
            raise ValueError('too few isolated weak English video-caption pairs')
    # Skip unavailable optional positive-only supervision rather than
    # inventing negatives or aborting a reviewed corpus with no such rows.
    if not extra_enabled:
        extra = []
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
        'positive_only_policy': POSITIVE_ONLY_POLICY,
        'positive_only_pairs_sha256': extra_sha,
        'positive_only_selected': len(extra),
        'positive_only_cap': args.positive_only_pairs,
        'positive_only_batch': args.positive_only_batch,
        'positive_only_every': args.positive_only_every,
        'positive_only_weight': args.positive_only_weight,
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
    if local_research:
        settings.update(quarantined_research_only=True, private_local_only=True,
                        production_promotion_allowed=False)
    if video_cap:
        settings.update(video_caption_policy=VIDEO_CAPTION_POLICY,
                        video_caption_pairs_sha256=video_sha,
                        video_caption_cap=video_cap, video_caption_selected=len(video),
                        video_caption_batch=video_batch, video_caption_every=video_every,
                        video_caption_weight=video_weight)
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
    early_stop_restarts = 0
    continuation_phase = None
    if args.resume:
        checkpoint = torch.load(last, weights_only=True, map_location='cpu')
        if checkpoint.get('format') != STAGE_FORMAT:
            raise ValueError('retrieval checkpoint format mismatch')
        continuation_phase, extended = resume_policy(
            checkpoint, settings, extend=getattr(args, 'extend_steps', False))
        model.load_state_dict(checkpoint['model'])
        optimizer.load_state_dict(checkpoint['optimizer'])
        torch.set_rng_state(checkpoint['cpu_rng'])
        torch.cuda.set_rng_state_all(checkpoint['cuda_rng'])
        generator.set_state(checkpoint['sampler_rng'])
        step, best_metrics, best_step, stale = (
            checkpoint['step'], checkpoint['best_metrics'],
            checkpoint['best_step'], checkpoint['stale'])
        early_stop_restarts = checkpoint.get('early_stop_restarts', 0)
        if type(early_stop_restarts) is not int or early_stop_restarts < 0:
            raise ValueError('invalid early-stop restart count')
        if extended:
            stale = 0  # Keep best health metrics and checkpoint across phases.
        else:
            retry = getattr(args, 'resume_after_early_stop', False)
            stale = resume_staleness(stale, args.patience, step, args.steps, reset=retry)
            early_stop_restarts += int(retry)

    print(json.dumps({
        'run_id': RUN_ID, 'stage': 'retrieval-anti-collapse',
        'architecture_params': sum(p.numel() for p in model.parameters()),
        'train_samples': len(train), 'validation_samples': len(val),
        'curation_counts': train_stats,
        'positive_only_curation_counts': extra_stats,
        'positive_only_enabled': extra_enabled,
        'weak_video_caption_counts': video_stats,
        'weak_video_caption_enabled': bool(video),
        'gpu_allocator_fraction': 0.45,
        'quarantined_research_only': local_research,
        'production_promotion_allowed': False,
        'continuation_phase': continuation_phase,
        'early_stop_restarts': early_stop_restarts,
        'selection_policy': SELECTION_POLICY,
    }), flush=True)
    device = torch.device('cuda')
    torch.cuda.reset_peak_memory_stats(device)
    for step in range(step + 1, args.steps + 1):
        step_started = time.monotonic()
        chosen = torch.randperm(len(train), generator=generator)[:args.batch_size]
        batch = [train[int(i)] for i in chosen]
        phase = configure_trainable_stage(model, step, args.projection_only_steps)
        optimizer.lr = (args.lr if phase == 'projection-only'
                        else args.lr * 0.25) * extension_lr_multiplier(
                            step, continuation_phase)
        optimizer.zero_grad()
        loss, measures = cached_retrieval_backward(model, batch, device,
                                                    optimizer=optimizer)
        extra_loss, extra_measures = None, None
        if extra_enabled and step % args.positive_only_every == 0:
            positive_sample = torch.randperm(
                len(extra), generator=generator)[:args.positive_only_batch]
            extra_loss, extra_measures = cached_positive_only_backward(
                model, [extra[int(i)] for i in positive_sample], device,
                optimizer=optimizer, weight=args.positive_only_weight)
        video_loss = None
        if video and step % video_every == 0:
            video_sample = torch.randperm(len(video), generator=generator)[:video_batch]
            video_loss, _ = cached_positive_only_backward(
                model, [video[int(i)] for i in video_sample], device,
                optimizer=optimizer, weight=video_weight)
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
                    'selection_policy': SELECTION_POLICY,
                    'continuation_phase': continuation_phase,
                    'model': model.state_dict(),
                })
            else:
                stale += 1
        if check or step % args.save_every == 0:
            save_atomic(last, {
                'format': STAGE_FORMAT, 'settings': settings,
                'selection_policy': SELECTION_POLICY,
                'step': step, 'model': model.state_dict(),
                'optimizer': optimizer.state_dict(),
                'sampler_rng': generator.get_state(),
                'continuation_phase': continuation_phase,
                'cpu_rng': torch.get_rng_state(),
                'cuda_rng': torch.cuda.get_rng_state_all(),
                'best_metrics': best_metrics, 'best_step': best_step,
                'stale': stale, 'early_stop_restarts': early_stop_restarts,
            })
            print(json.dumps({
                'stage': 'retrieval-anti-collapse', 'step': step,
                'step_elapsed_seconds': round(time.monotonic() - step_started, 3),
                'cuda_peak_allocated_mib': round(torch.cuda.max_memory_allocated(device) / 1024**2, 1),
                'train_objective': round(loss, 6),
                'positive_only_objective': (round(extra_loss, 6)
                                            if extra_loss is not None else None),
                'weak_video_caption_objective': (round(video_loss, 6)
                                                 if video_loss is not None else None),
                'positive_only_geometry': ({k: round(v, 6)
                                             for k, v in extra_measures.items()}
                                            if extra_measures is not None else None),
                'phase': phase,
                'learning_rate': optimizer.lr,
                'train_geometry': {k: round(v, 6) for k, v in measures.items()},
                'val_metrics': val_metrics,
                'healthy': (checkpoint_is_healthy(val_metrics)
                            if val_metrics is not None else None),
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
    p.add_argument('--positive-only-pairs', type=int, default=256,
                   help='deterministic positive-only candidate pool cap; 0 disables')
    p.add_argument('--positive-only-batch', type=int, default=2)
    p.add_argument('--positive-only-every', type=int, default=4)
    p.add_argument('--positive-only-weight', type=float, default=0.25)
    p.add_argument('--video-caption-pairs', type=int, default=0,
                   help='private-research weak English positives from same-video captions; 0 disables')
    p.add_argument('--video-caption-batch', type=int, default=2)
    p.add_argument('--video-caption-every', type=int, default=8)
    p.add_argument('--video-caption-weight', type=float, default=0.1)
    p.add_argument('--seed', type=int, default=1001)
    p.add_argument('--resume', action='store_true')
    p.add_argument('--resume-after-early-stop', action='store_true',
                   help='explicitly reset patience for an unfinished early-stopped run')
    p.add_argument('--local-research-quarantine', action='store_true',
                   help='private local research only; neither data nor weights are approved for release')
    p.add_argument('--extend-steps', action='store_true',
                   help='after completed budget, continue identical corpus and tokenizer at lower LR')
    args = p.parse_args()
    result = run(args)
    print(json.dumps({
        'run_id': RUN_ID, 'stage': 'retrieval-anti-collapse',
        'best_checkpoint': str(result) if result else None,
        'promoted_to_production': False,
    }), flush=True)


if __name__ == '__main__':
    main()
