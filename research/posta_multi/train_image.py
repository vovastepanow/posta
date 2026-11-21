"""Experimental image-text alignment on verified local train examples."""
import argparse
import hashlib
import json
from dataclasses import asdict
from pathlib import Path

import torch
from PIL import Image
from tokenizers import Tokenizer
from corpus import Corpus
from corpus_smoke import rgb
from cpu_master_sgd import CPUMasterSGD
from loss import retrieval_loss
from model import PostaMulti
from spec import Config
from train_text import save_atomic


def load_rows(corpus, limit):
    rows = []
    seen_text, seen_media = set(), set()
    for row in corpus.rows('image_text', 'train'):
        media = row.get('media') or {}
        text, sha = row['text'], media.get('sha256')
        if not text.strip() or not sha or text in seen_text or sha in seen_media:
            continue
        seen_text.add(text)
        seen_media.add(sha)
        rows.append(row)
        if len(rows) == limit:
            break
    if len(rows) != limit:
        raise ValueError('not enough distinct image-text records')
    data = json.dumps([(r['dataset_id'], r['dataset_revision'], r['text'], r['media']['sha256'])
                       for r in rows], ensure_ascii=False, separators=(',', ':')).encode()
    return rows, hashlib.sha256(data).hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--init', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--samples', type=int, default=128)
    p.add_argument('--steps', type=int, required=True)
    p.add_argument('--save-every', type=int, default=8)
    p.add_argument('--resume', action='store_true')
    args = p.parse_args()
    if args.steps < 1 or args.samples < 2 or args.save_every < 1:
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
    rows, rows_hash = load_rows(corpus, args.samples)
    tokenizer_hash = hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=128)
    with open(args.init, 'rb') as f:
        initial_hash = hashlib.file_digest(f, 'sha256').hexdigest()
    initial = torch.load(args.init, map_location='cpu', weights_only=True)
    if initial['settings']['tokenizer'] != tokenizer_hash or initial['settings']['manifest_sha256'] != corpus.manifest_hash:
        raise ValueError('initial checkpoint differs from corpus or tokenizer')
    config = Config(**initial['settings']['config'])
    settings = {'config': asdict(config), 'tokenizer': tokenizer_hash, 'manifest_sha256': corpus.manifest_hash,
                'init_sha256': initial_hash, 'rows_sha256': rows_hash, 'samples': args.samples,
                'max_text_length': 128, 'objective': 'two-pair-image-text-v1'}
    model = PostaMulti(config).half().cuda()
    model.load_state_dict(initial['model'])
    del initial
    optimizer = CPUMasterSGD(model.parameters(), lr=0.001, loss_scale=16)
    step = 0
    if args.resume:
        state = torch.load(output, map_location='cpu', weights_only=True)
        if state.get('format') != 'posta-image-v1' or state['settings'] != settings:
            raise ValueError('image checkpoint settings differ')
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
        indices = torch.randperm(len(rows))[:2].tolist()
        batch = [rows[i] for i in indices]
        optimizer.zero_grad()
        queries, images = [], []
        for row in batch:
            ids = torch.tensor([tokenizer.encode(row['text']).ids], device='cuda')
            queries.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)))
            with Image.open(corpus.media_path(row)) as image:
                images.append(model.encode_image(rgb(image, config.image_size).half().cuda()))
        loss = retrieval_loss(torch.cat(queries), torch.cat(images), torch.eye(2, dtype=torch.bool, device='cuda'))
        if not torch.isfinite(loss):
            raise RuntimeError('non-finite loss; previous checkpoint preserved')
        optimizer.backward(loss)
        if not optimizer.step():
            raise RuntimeError('gradient overflow; previous checkpoint preserved')
        if step % args.save_every == 0 or step == args.steps:
            save_atomic(output, {'format': 'posta-image-v1', 'settings': settings, 'step': step,
                                 'model': model.state_dict(), 'optimizer': optimizer.state_dict(),
                                 'cpu_rng': torch.get_rng_state(), 'cuda_rng': torch.cuda.get_rng_state_all()})
            print(json.dumps({'saved_step': step, 'last_loss': float(loss.detach().cpu())}), flush=True)


if __name__ == '__main__':
    main()
