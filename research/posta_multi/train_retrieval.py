"""Experimental explicit-negative retrieval training for Posta Multi."""
import argparse
import hashlib
import json
from dataclasses import asdict
from pathlib import Path

import torch
from tokenizers import Tokenizer
from corpus import Corpus
from cpu_master_sgd import CPUMasterSGD
from model import PostaMulti
from retrieval_stage import load_pairs, pair_loss
from spec import Config
from train_text import save_atomic


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--init', required=True, help='text checkpoint for initial weights')
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--samples', type=int, default=128)
    p.add_argument('--steps', type=int, required=True)
    p.add_argument('--save-every', type=int, default=8)
    p.add_argument('--resume', action='store_true')
    args = p.parse_args()
    if args.steps < 1 or args.samples < 1 or args.save_every < 1:
        raise ValueError('invalid sample/step count')
    output = Path(args.checkpoint)
    if output.exists() and not args.resume:
        raise FileExistsError('checkpoint exists; use --resume')
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA required')
    torch.set_num_threads(2)
    torch.manual_seed(42)
    torch.cuda.manual_seed_all(42)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    pairs, pairs_hash = load_pairs(corpus, args.samples)
    tok_hash = hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    with open(args.init, 'rb') as f:
        source_hash = hashlib.file_digest(f, 'sha256').hexdigest()
    source = torch.load(args.init, map_location='cpu', weights_only=True)
    config = Config(**source['settings']['config'])
    if source['settings']['tokenizer'] != tok_hash or source['settings']['manifest_sha256'] != corpus.manifest_hash:
        raise ValueError('initial checkpoint does not match tokenizer or corpus')
    settings = {'config': asdict(config), 'tokenizer': tok_hash, 'manifest_sha256': corpus.manifest_hash,
                'init_sha256': source_hash, 'pairs_sha256': pairs_hash, 'samples': args.samples,
                'max_length': 128, 'objective': 'explicit-negative-v1'}
    model = PostaMulti(config).half().cuda()
    model.load_state_dict(source['model'])
    del source
    optimizer = CPUMasterSGD(model.parameters(), lr=0.001, loss_scale=16)
    step = 0
    if args.resume:
        state = torch.load(output, map_location='cpu', weights_only=True)
        if state.get('format') != 'posta-retrieval-v1' or state['settings'] != settings:
            raise ValueError('retrieval checkpoint settings differ')
        step = state['step']
        if step > args.steps:
            raise ValueError('target step precedes checkpoint')
        model.load_state_dict(state['model'])
        optimizer.load_state_dict(state['optimizer'])
        torch.set_rng_state(state['cpu_rng'])
        torch.cuda.set_rng_state_all(state['cuda_rng'])
        del state
    model.train()
    for step in range(step + 1, args.steps + 1):
        pair = pairs[int(torch.randint(len(pairs), (1,)).item())]
        optimizer.zero_grad()
        loss = pair_loss(model, tokenizer, pair, 'cuda', max_length=128)
        if not torch.isfinite(loss):
            raise RuntimeError('non-finite loss; previous checkpoint preserved')
        optimizer.backward(loss)
        if not optimizer.step():
            raise RuntimeError('gradient overflow; previous checkpoint preserved')
        if step % args.save_every == 0 or step == args.steps:
            save_atomic(output, {'format': 'posta-retrieval-v1', 'settings': settings, 'step': step,
                                 'model': model.state_dict(), 'optimizer': optimizer.state_dict(),
                                 'cpu_rng': torch.get_rng_state(), 'cuda_rng': torch.cuda.get_rng_state_all()})
            print(json.dumps({'saved_step': step, 'last_loss': float(loss.detach().cpu())}), flush=True)


if __name__ == '__main__':
    main()
