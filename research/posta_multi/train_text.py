"""Resumable bounded OCR-text masked-byte training; experimental, not full curriculum.

Only NVIDIA OCR train rows are used. No production database is accessed.
"""
import argparse
import hashlib
import json
import os
import shutil
import tempfile
from dataclasses import asdict, replace
from pathlib import Path

import torch
import torch.nn.functional as F

if __package__:
    from .corpus import Corpus
    from .cpu_master_sgd import CPUMasterSGD
    from .cpu_master_adam import CPUMasterAdamW
    from .model import PostaMulti
    from .spec import Config
    from .synthetic_train import tiny_config
    from .tokenizer import CLS, SEP, MASK, VERSION, VOCAB_SIZE, encode
else:
    from corpus import Corpus
    from cpu_master_sgd import CPUMasterSGD
    from cpu_master_adam import CPUMasterAdamW
    from model import PostaMulti
    from spec import Config
    from synthetic_train import tiny_config
    from tokenizer import CLS, SEP, MASK, VERSION, VOCAB_SIZE, encode

SOURCES = {'nvidia-ocr-en', 'nvidia-ocr-ru'}
REVISION = '69696a1cc543ef3a0f8e9892a89c17293e915263'


def examples(corpus, count, length, encode_text=encode):
    result = []
    for row in corpus.rows('ocr_text', 'train'):
        if row['dataset_id'] not in SOURCES or row['dataset_revision'] != REVISION:
            continue
        text = row.get('ocr')
        if isinstance(text, str) and text.strip():
            ids = encode_text(text, length)
            if len(ids) > 2:
                result.append(ids)
        if len(result) == count:
            break
    if len(result) != count:
        raise ValueError('not enough matching OCR training records')
    digest = hashlib.sha256(json.dumps(result, separators=(',', ':')).encode()).hexdigest()
    return result, digest


def mixed_examples(corpus, count, length, encode_text):
    """Equal record quotas; split long token sequences into training windows."""
    quotas = {'text': ('ru', 'en'), 'ocr_text': ('ru', 'en'),
              'image_text': ('ru', 'en'), 'retrieval': ('ru',), 'video_text': ('en',)}
    windows = []
    counts = {}
    for kind, languages in quotas.items():
        counts[kind] = {language: 0 for language in languages}
        for row in corpus.rows(kind, 'train'):
            lang = row.get('language')
            if lang not in counts[kind] or counts[kind][lang] >= count:
                continue
            text = row['text'][:4096]
            ids = encode_text(text, 4096)
            if len(ids) <= 2 or ids[0] != CLS or ids[-1] != SEP:
                continue
            for start in range(1, len(ids) - 1, length - 2):
                windows.append([CLS, *ids[start:min(start + length - 2, len(ids) - 1)], SEP])
            counts[kind][lang] += 1
            if all(value == count for value in counts[kind].values()):
                break
        if any(value != count for value in counts[kind].values()):
            raise ValueError(f'insufficient mixed train records: {kind} {counts[kind]}')
    digest = hashlib.sha256(json.dumps(windows, separators=(',', ':')).encode()).hexdigest()
    return windows, digest


def save_atomic(path, payload):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.posta-checkpoint-', suffix='.tmp', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as f:
            torch.save(payload, f)
            f.flush()
            os.fsync(f.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def run(dataset, checkpoint, steps, *, resume=False, model_size='tiny',
        samples=8, seed=42, save_every=1, lr=0.001, tokenizer_path=None, data_mode='ocr', warm_start=None, text_only_adam=False):
    if resume and warm_start:
        raise ValueError('choose either resume or warm start')
    if steps < 1 or not 1 <= samples <= 10000 or save_every < 1:
        raise ValueError('invalid steps, sample count or save interval')
    if text_only_adam and model_size != 'full':
        raise ValueError('text-only Adam requires full model')
    if data_mode not in ('ocr', 'mixed'):
        raise ValueError('invalid data mode')
    if model_size not in ('tiny', 'full'):
        raise ValueError('model size must be tiny or full')
    path = Path(checkpoint)
    if path.exists() and not resume:
        raise FileExistsError('checkpoint exists; choose a new path or resume')
    if resume and not path.is_file():
        raise FileNotFoundError('resume checkpoint is missing')
    torch.set_num_threads(2)
    torch.manual_seed(seed)
    tokenizer_id = VERSION
    encode_text = encode
    vocab_size = VOCAB_SIZE
    if tokenizer_path is not None:
        from tokenizers import Tokenizer
        tokenizer_file = Path(tokenizer_path)
        metadata = json.loads((tokenizer_file.parent / 'metadata.json').read_text(encoding='utf-8'))
        tokenizer_id = hashlib.sha256(tokenizer_file.read_bytes()).hexdigest()
        if tokenizer_id != metadata['tokenizer_sha256']:
            raise ValueError('tokenizer hash mismatch')
        tokenizer = Tokenizer.from_file(str(tokenizer_file))
        if any(tokenizer.token_to_id(t) != i for i, t in enumerate(('[PAD]', '[CLS]', '[SEP]', '[MASK]'))):
            raise ValueError('tokenizer special token IDs differ')
        vocab_size = tokenizer.get_vocab_size()
        if vocab_size > Config().vocab_size:
            raise ValueError('tokenizer exceeds model vocabulary')
        def encode_text(text, length):
            tokenizer.enable_truncation(max_length=length)
            return tokenizer.encode(text).ids
    config = replace(tiny_config(), vocab_size=vocab_size) if model_size == 'tiny' else Config()
    corpus = Corpus(dataset)
    encoded, encoded_hash = (examples(corpus, samples, config.text_length, encode_text)
                             if data_mode == 'ocr' else mixed_examples(corpus, samples, config.text_length, encode_text))
    settings = {'config': asdict(config), 'model_size': model_size, 'tokenizer': tokenizer_id,
                'manifest_sha256': corpus.manifest_hash, 'examples_sha256': encoded_hash,
                'samples': samples, 'seed': seed, 'lr': lr, 'data_kind': 'nvidia-ocr-train' if data_mode == 'ocr' else 'mixed-train-v1',
                'objective': 'masked-token-15pct-max64-v1'}
    if text_only_adam:
        settings['optimizer'] = 'text-only-cpu-adamw-v1'
    if model_size == 'full':
        if not torch.cuda.is_available():
            raise RuntimeError('full-size experiment requires CUDA')
        # Atomic replacement temporarily needs both old and new checkpoint files.
        path.parent.mkdir(parents=True, exist_ok=True)
        if shutil.disk_usage(path.parent).free < 4 * 1024 ** 3:
            raise RuntimeError('at least 4 GiB free disk space required for checkpoint')
        torch.cuda.set_per_process_memory_fraction(0.45)
        torch.cuda.manual_seed_all(seed)
        model = PostaMulti(config).half().cuda()
        if text_only_adam:
            names = ('text_tokens', 'text_positions', 'text_encoder', 'text_norm')
            for name, parameter in model.named_parameters():
                parameter.requires_grad_(any(name == prefix or name.startswith(prefix + '.') for prefix in names))
            optimizer = CPUMasterAdamW((p for p in model.parameters() if p.requires_grad), lr=lr)
        else:
            optimizer = CPUMasterSGD(model.parameters(), lr=lr, loss_scale=16)
        device = 'cuda'
    else:
        model = PostaMulti(config)
        optimizer = torch.optim.AdamW(model.parameters(), lr=lr)
        device = 'cpu'
    step = 0
    origin = None
    if warm_start:
        warm_path = Path(warm_start)
        with warm_path.open('rb') as stream:
            warm_hash = hashlib.file_digest(stream, 'sha256').hexdigest()
        initial = torch.load(warm_path, map_location='cpu', weights_only=True)
        prior = initial['settings']
        if initial.get('format') not in ('posta-text-v1', 'posta-image-v1', 'posta-retrieval-v1', 'posta-media-v1') or prior['config'] != settings['config'] or prior['tokenizer'] != settings['tokenizer']:
            raise ValueError('warm-start architecture or tokenizer mismatch')
        model.load_state_dict(initial['model'])
        if text_only_adam:
            for param, master in zip(optimizer.params, optimizer.masters):
                master.copy_(param.detach().to(device='cpu', dtype=torch.float32))
        else:
            optimizer.load_state_dict(initial['optimizer'])
        origin = {'checkpoint_sha256': warm_hash, 'step': initial['step']}
        del initial
    if resume:
        state = torch.load(path, map_location='cpu', weights_only=True)
        if state.get('format') != 'posta-text-v1' or state['settings'] != settings:
            raise ValueError('checkpoint settings, tokenizer or data differ')
        step = state['step']
        origin = state.get('origin')
        if step > steps:
            raise ValueError('target step precedes checkpoint')
        model.load_state_dict(state['model'])
        optimizer.load_state_dict(state['optimizer'])
        torch.set_rng_state(state['cpu_rng'])
        if device == 'cuda':
            torch.cuda.set_rng_state_all(state['cuda_rng'])
        del state
    model.train()
    last_loss = None
    for step in range(step + 1, steps + 1):
        index = int(torch.randint(len(encoded), (1,)).item())
        ids = torch.tensor([encoded[index]], dtype=torch.long, device=device)
        mask = torch.ones_like(ids, dtype=torch.bool)
        # Randomly mask 15% of content tokens, capped to bound vocabulary logits.
        eligible = ids.shape[1] - 2
        n_masked = min(64, max(1, round(0.15 * eligible)))
        positions = torch.randperm(eligible)[:n_masked].to(device) + 1
        selected = torch.zeros_like(ids, dtype=torch.bool)
        selected[0, positions] = True
        target = ids[selected].clone()
        ids[selected] = MASK
        if model_size == 'full':
            optimizer.zero_grad()
        else:
            optimizer.zero_grad(set_to_none=True)
        loss = F.cross_entropy(model.masked_token_logits(ids, mask, selected).float(), target)
        if not torch.isfinite(loss):
            raise RuntimeError('non-finite loss; last checkpoint remains intact')
        if model_size == 'full':
            optimizer.backward(loss)
            if not optimizer.step():
                raise RuntimeError('FP16 gradient overflow; last checkpoint remains intact')
        else:
            loss.backward()
            if any(p.grad is not None and not torch.isfinite(p.grad).all() for p in model.parameters()):
                raise RuntimeError('non-finite gradient; last checkpoint remains intact')
            optimizer.step()
        last_loss = float(loss.detach().cpu())
        if step % save_every == 0 or step == steps:
            state = {'format': 'posta-text-v1', 'settings': settings, 'step': step,
                     'model': model.state_dict(), 'optimizer': optimizer.state_dict(), 'origin': origin,
                     'cpu_rng': torch.get_rng_state(),
                     'cuda_rng': torch.cuda.get_rng_state_all() if device == 'cuda' else []}
            save_atomic(path, state)
            print(json.dumps({'saved_step': step, 'last_loss': last_loss}), flush=True)
    return {'step': steps, 'last_loss': last_loss, 'model_size': model_size,
            'dataset_manifest_sha256': corpus.manifest_hash, 'checkpoint': str(path)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--steps', type=int, required=True, help='total steps after resume')
    parser.add_argument('--resume', action='store_true')
    parser.add_argument('--model-size', choices=('tiny', 'full'), default='tiny')
    parser.add_argument('--samples', type=int, default=8)
    parser.add_argument('--save-every', type=int, default=1)
    parser.add_argument('--tokenizer', help='experimental tokenizer.json with adjacent metadata.json')
    parser.add_argument('--data-mode', choices=('ocr', 'mixed'), default='ocr')
    parser.add_argument('--warm-start', help='initialize from a compatible earlier checkpoint')
    parser.add_argument('--text-only-adam', action='store_true')
    parser.add_argument('--learning-rate', type=float, default=0.001)
    args = parser.parse_args()
    print(json.dumps(run(args.dataset, args.checkpoint, args.steps, resume=args.resume,
                         model_size=args.model_size, samples=args.samples, save_every=args.save_every,
                         tokenizer_path=args.tokenizer, data_mode=args.data_mode, warm_start=args.warm_start,
                         text_only_adam=args.text_only_adam, lr=args.learning_rate)))


if __name__ == '__main__':
    main()
