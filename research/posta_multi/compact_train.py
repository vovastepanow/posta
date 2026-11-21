"""Train the compact scratch retriever in bounded FP32 batches.

Research-only. Requires a dataset explicitly marked approved_for_training;
the existing screened/quarantined Posta dataset is deliberately NOT accepted.
No cron or production service invokes this trainer. The test split is never
used for model selection. No pretrained weights are loaded.
"""
import argparse
import hashlib
import json
import math
from dataclasses import asdict, replace
from pathlib import Path

import torch
from tokenizers import Tokenizer

from compact import CompactConfig, CompactRetriever, paired_contrastive_loss
from compare_checkpoints import atomic_report
from corpus import Corpus
from ranking_metrics import conservative_rank, summarize_ranks
from retrieval_data_quality import CURATION_POLICY, curate_retrieval_pairs
from train_text import save_atomic


def require_training_approval(manifest):
    if not isinstance(manifest, dict) or manifest.get('status') != 'approved_for_training':
        raise ValueError('dataset is quarantined or not explicitly approved for training')
    if manifest.get('privacy_review') != 'passed' or        manifest.get('source_rights_review') != 'passed':
        raise ValueError('training requires explicit privacy and source-rights review')


def encode_pairs(tokenizer, pairs, max_length):
    tokenizer.enable_truncation(max_length=max_length)
    result = []
    for triple in pairs:
        seqs = []
        for text in triple:
            ids = tokenizer.encode(text).ids
            if len(ids) < 3 or ids[0] != 1 or ids[-1] != 2:
                raise ValueError('tokenizer output missing CLS/content/SEP')
            seqs.append(tuple(ids))
        result.append(tuple(seqs))
    return result


def batch_tensors(sequences, device):
    if not sequences:
        raise ValueError('empty batch')
    width = max(map(len, sequences))
    token_ids = torch.zeros(len(sequences), width, dtype=torch.long)
    attention = torch.zeros(len(sequences), width, dtype=torch.bool)
    for i, seq in enumerate(sequences):
        if len(seq) < 3:
            raise ValueError('invalid token sequence')
        token_ids[i, :len(seq)] = torch.tensor(seq, dtype=torch.long)
        attention[i, :len(seq)] = True
    return token_ids.to(device), attention.to(device)


def batch_loss(model, triples, device, temperature=0.10):
    n = len(triples)
    if n < 1:
        raise ValueError('empty retrieval batch')
    # One joint forward pass improves throughput while each query is trained
    # only against its explicit negative, not potentially relevant in-batch docs.
    flattened = ([triplet[0] for triplet in triples] +
                 [triplet[1] for triplet in triples] +
                 [triplet[2] for triplet in triples])
    ids, mask = batch_tensors(flattened, device)
    q, p, negative = model.encode(ids, mask).split(n)
    return paired_contrastive_loss(q, p, negative, temperature)


@torch.no_grad()
def validate(model, triples, device, batch_size=8):
    """Local validation pool; not independent real-world search quality."""
    if len(triples) < 2:
        raise ValueError('need multiple held-out triples')
    documents = list(dict.fromkeys(sequence for _, positive, negative in triples
                                    for sequence in (positive, negative)))
    index = {doc: i for i, doc in enumerate(documents)}
    queries = list(dict.fromkeys(t[0] for t in triples))

    def embedding_batch(sequences):
        embeds = []
        for offset in range(0, len(sequences), batch_size):
            ids, mask = batch_tensors(sequences[offset:offset + batch_size], device)
            embeds.append(model.encode(ids, mask).float().cpu())
        return torch.cat(embeds, dim=0)

    previous = model.training
    model.eval()
    try:
        d_embeddings = embedding_batch(documents)
        q_embeddings = embedding_batch(queries)
    finally:
        model.train(previous)
    query_index = {seq: i for i, seq in enumerate(queries)}
    ranks = []
    wins = 0
    for query, pos, negative in triples:
        scores = (q_embeddings[query_index[query]] @ d_embeddings.T).tolist()
        wins += scores[index[pos]] > scores[index[negative]]
        ranks.append(conservative_rank(scores, index[pos]))
    return {
        'samples': len(triples),
        'documents': len(documents),
        'pair_accuracy': round(wins / len(triples), 4),
        **summarize_ranks(ranks),
    }


def learning_rate(step, total, base_rate, warmup):
    if not 1 <= step <= total or total < 1 or not base_rate > 0 or warmup < 0:
        raise ValueError('invalid learning-rate schedule')
    if warmup and step <= warmup:
        return base_rate * step / warmup
    span = max(1, total - warmup)
    phase = max(0, step - warmup) / span
    return base_rate * (0.1 + 0.9 * 0.5 * (1.0 + math.cos(math.pi * min(phase, 1.0))))


def better(metrics, best):
    """Require MRR gain with no material pair-accuracy regression."""
    return best is None or (
        metrics['mean_reciprocal_rank'] > best['mean_reciprocal_rank'] + 0.001
        and metrics['pair_accuracy'] >= best['pair_accuracy'] - 0.02
    )


def run(args):
    if args.steps < 1 or args.batch_size < 1 or args.eval_every < 1 or        args.train_pairs < 2 or args.val_pairs < 2 or args.patience < 1 or        not 0 < args.lr <= 0.01 or not 0 < args.temperature <= 2 or        not 0 < args.memory_fraction <= 1 or args.warmup < 0 or args.warmup >= args.steps:
        raise ValueError('invalid training arguments')
    if args.train_pairs < args.batch_size:
        raise ValueError('train_pairs must be >= batch_size')

    output = Path(args.output)
    last = output / 'compact-last.pt'
    best_path = output / 'compact-best.pt'
    if not args.resume and (last.exists() or best_path.exists()):
        raise FileExistsError('an earlier compact experiment exists; use --resume or new output')

    corpus = Corpus(args.dataset)
    require_training_approval(corpus.manifest)
    tokenizer_bytes = Path(args.tokenizer).read_bytes()
    tokenizer_sha = hashlib.sha256(tokenizer_bytes).hexdigest()
    tokenizer = Tokenizer.from_file(str(args.tokenizer))
    for special, expected in (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2), ('[MASK]', 3)):
        if tokenizer.token_to_id(special) != expected:
            raise ValueError(f'special token ID mismatch: {special}')
    config = replace(CompactConfig(), vocab_size=tokenizer.get_vocab_size(),
                     max_length=args.max_length)
    training, train_sha, counts = curate_retrieval_pairs(
        corpus, args.train_pairs, split='train', avoid_heldout=True)
    validation, val_sha, _ = curate_retrieval_pairs(
        corpus, args.val_pairs, split='validation', avoid_heldout=False)
    train_ids = encode_pairs(tokenizer, training, config.max_length)
    val_ids = encode_pairs(tokenizer, validation, config.max_length)
    metadata = {
        'format': 'posta-compact-retrieval-v1',
        'config': asdict(config),
        'dataset_sha256': corpus.manifest_hash,
        'tokenizer_sha256': tokenizer_sha,
        'train_pairs_sha256': train_sha,
        'val_pairs_sha256': val_sha,
        'curation_policy': CURATION_POLICY,
        'steps': args.steps,
        'train_pairs': args.train_pairs,
        'val_pairs': args.val_pairs,
        'batch_size': args.batch_size,
        'lr': args.lr,
        'warmup': args.warmup,
        'temperature': args.temperature,
        'seed': args.seed,
    }
    torch.set_num_threads(2)
    torch.manual_seed(args.seed)
    device = torch.device('cuda' if torch.cuda.is_available() and not args.cpu else 'cpu')
    if device.type == 'cuda':
        torch.cuda.manual_seed_all(args.seed)
        torch.cuda.set_per_process_memory_fraction(args.memory_fraction)
    model = CompactRetriever(config).to(device=device, dtype=torch.float32)
    opt = torch.optim.AdamW(model.parameters(), lr=args.lr, weight_decay=0.01)
    sampler = torch.Generator(device='cpu').manual_seed(args.seed + 10_000)

    start = 0
    best_metrics = None
    best_step = 0
    stale = 0
    if args.resume:
        if not last.is_file():
            raise FileNotFoundError('cannot resume without compact-last.pt')
        data = torch.load(last, weights_only=True, map_location='cpu')
        if data.get('settings') != metadata or data.get('format') != 'posta-compact-retrieval-v1':
            raise ValueError('incompatible checkpoint, tokenizer, dataset or training settings')
        start = data['step']
        if start >= args.steps:
            raise ValueError('checkpoint already completed requested training')
        model.load_state_dict(data['model'])
        opt.load_state_dict(data['optimizer'])
        torch.set_rng_state(data['cpu_rng'])
        if device.type == 'cuda':
            if not data.get('cuda_rng'):
                raise ValueError('checkpoint is missing CUDA RNG state')
            torch.cuda.set_rng_state_all(data['cuda_rng'])
        sampler.set_state(data['sampler_rng'])
        best_metrics, best_step = data['best_metrics'], data['best_step']
        stale = data['stale']
        del data

    output.mkdir(parents=True, exist_ok=True)
    print(json.dumps({'stage': 'compact-start', 'device': str(device),
                      'train_samples': len(train_ids), 'val_samples': len(val_ids),
                      'parameters': sum(p.numel() for p in model.parameters()),
                      'approval': 'verified', 'curation_counts': counts}), flush=True)
    for step in range(start + 1, args.steps + 1):
        model.train()
        sampled = torch.randint(len(train_ids), (args.batch_size,), generator=sampler)
        batch = [train_ids[int(index)] for index in sampled]
        rate = learning_rate(step, args.steps, args.lr, args.warmup)
        for group in opt.param_groups:
            group['lr'] = rate
        opt.zero_grad(set_to_none=True)
        loss = batch_loss(model, batch, device, args.temperature)
        if not bool(torch.isfinite(loss)):
            raise RuntimeError('non-finite loss; previous checkpoint preserved')
        loss.backward()
        torch.nn.utils.clip_grad_norm_(model.parameters(), max_norm=1.0,
                                       error_if_nonfinite=True)
        opt.step()

        if step % args.eval_every == 0 or step == args.steps:
            metrics = validate(model, val_ids, device, args.batch_size)
            improved = better(metrics, best_metrics)
            if improved:
                best_metrics = dict(metrics)
                best_step = step
                stale = 0
                save_atomic(best_path, {
                    'format': 'posta-compact-best-v1',
                    'settings': metadata, 'step': step,
                    'metrics': metrics, 'model': model.state_dict(),
                })
            else:
                stale += 1
            save_atomic(last, {
                'format': 'posta-compact-retrieval-v1', 'settings': metadata,
                'step': step, 'model': model.state_dict(),
                'optimizer': opt.state_dict(), 'cpu_rng': torch.get_rng_state(),
                'cuda_rng': torch.cuda.get_rng_state_all() if device.type == 'cuda' else [],
                'sampler_rng': sampler.get_state(),
                'best_metrics': best_metrics, 'best_step': best_step, 'stale': stale,
            })
            print(json.dumps({'step': step, 'train_loss': round(float(loss.detach().cpu()), 6),
                              'val': metrics, 'best_step': best_step,
                              'improved': improved}), flush=True)
            if stale >= args.patience:
                print(json.dumps({'stopped_early_at': step,
                                  'best_step': best_step}), flush=True)
                break
    return best_path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--steps', type=int, default=1200)
    parser.add_argument('--batch-size', type=int, default=8)
    parser.add_argument('--train-pairs', type=int, default=4096)
    parser.add_argument('--val-pairs', type=int, default=64)
    parser.add_argument('--max-length', type=int, default=160)
    parser.add_argument('--eval-every', type=int, default=50)
    parser.add_argument('--patience', type=int, default=8)
    parser.add_argument('--warmup', type=int, default=50)
    parser.add_argument('--lr', type=float, default=0.0003)
    parser.add_argument('--temperature', type=float, default=0.10)
    parser.add_argument('--memory-fraction', type=float, default=0.65)
    parser.add_argument('--seed', type=int, default=42)
    parser.add_argument('--cpu', action='store_true', help='CPU-only debugging')
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    print(json.dumps({'best_checkpoint': str(run(args))}), flush=True)


if __name__ == '__main__':
    main()
