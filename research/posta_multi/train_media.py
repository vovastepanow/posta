"""Short experimental video/audio-text alignment on real local media."""
import argparse
import hashlib
import json
import subprocess
from dataclasses import asdict
from pathlib import Path

import numpy as np
import torch
import torch.nn.functional as F
from torch.utils.checkpoint import checkpoint
from tokenizers import Tokenizer
from corpus import Corpus
from corpus_smoke import ffmpeg, log_mel
from cpu_master_sgd import CPUMasterSGD
from cpu_master_adam import CPUMasterAdamW
from loss import retrieval_loss
from model import PostaMulti
from spec import Config
from train_text import save_atomic


def audio_duration(path):
    # FLAC STREAMINFO stores the sample rate and sample count in its first block.
    with path.open('rb') as stream:
        header = stream.read(26)
    if len(header) == 26 and header[:4] == b'fLaC' and header[4] & 127 == 0 and int.from_bytes(header[5:8], 'big') == 34:
        packed = int.from_bytes(header[18:26], 'big')
        rate, samples = packed >> 44, packed & ((1 << 36) - 1)
        if rate and samples:
            return samples / rate
    result = subprocess.run(['ffprobe', '-v', 'error', '-show_entries', 'format=duration',
                             '-of', 'default=noprint_wrappers=1:nokey=1', str(path)],
                            capture_output=True, text=True, check=True, timeout=20)
    return float(result.stdout.strip())


def load_rows(corpus, kind, limit, max_audio_seconds=12.0):
    rows, seen_text, seen_media = [], set(), set()
    candidates = corpus.rows(kind, 'train')
    if kind == 'speech_text':
        candidates = sorted(candidates, key=lambda row: (row.get('media') or {}).get('size_bytes', float('inf')))
    for row in candidates:
        sha = (row.get('media') or {}).get('sha256')
        text = row['text']
        if not sha or not text.strip() or sha in seen_media or text in seen_text:
            continue
        if kind == 'speech_text':
            path = corpus.media_path(row)
            duration = audio_duration(path)
            if not 0.1 <= duration <= max_audio_seconds:
                continue
        rows.append(row)
        seen_text.add(text)
        seen_media.add(sha)
        if len(rows) == limit:
            break
    if len(rows) != limit:
        raise ValueError('not enough distinct media records')
    data = json.dumps([(x['dataset_id'], x['dataset_revision'], x['text'], x['media']['sha256'])
                       for x in rows], ensure_ascii=False, separators=(',', ':')).encode()
    return rows, hashlib.sha256(data).hexdigest()


def encode_complete_audio(model, mel):
    parameter = next(model.parameters())
    segments = []
    for offset in range(0, mel.shape[-1], 256):
        segment = mel[:, :, offset:offset+256].to(device=parameter.device, dtype=parameter.dtype)
        segments.append(checkpoint(model.encode_audio, segment, use_reentrant=False))
    if not segments:
        raise ValueError('empty audio')
    return F.normalize(torch.stack(segments).mean(dim=0), dim=-1, eps=1e-6)


def media_embedding(model, corpus, row, kind, config):
    path = corpus.media_path(row)
    if kind == 'speech_text':
        mel = log_mel(path, bins=config.mel_bins, duration=None)
        return encode_complete_audio(model, mel)
    size = config.image_size
    data = ffmpeg('-i', path, '-vf', f'fps=1,scale={size}:{size}', '-frames:v', '2',
                  '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1')
    frame_size = size * size * 3
    if len(data) < frame_size or len(data) % frame_size:
        raise ValueError('could not decode video frames')
    frames = np.frombuffer(data, dtype=np.uint8).copy().reshape(-1, size, size, 3)
    tensor = torch.from_numpy(frames).permute(0, 3, 1, 2).unsqueeze(0).half().cuda() / 255
    return model.encode_video(tensor)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--init', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--kind', choices=('speech_text', 'video_text'), required=True)
    p.add_argument('--samples', type=int, default=32)
    p.add_argument('--batch-size', type=int, default=2)
    p.add_argument('--audio-max-seconds', type=float, default=12.0)
    p.add_argument('--steps', type=int, required=True)
    p.add_argument('--save-every', type=int, default=4)
    p.add_argument('--resume', action='store_true')
    p.add_argument('--audio-only-adam', action='store_true')
    args = p.parse_args()
    if args.audio_only_adam and args.kind != 'speech_text':
        raise ValueError('audio-only Adam requires speech_text')
    if not 0.1 < args.audio_max_seconds <= 30:
        raise ValueError('audio-max-seconds must be between 0.1 and 30')
    if args.steps < 1 or args.batch_size < 2 or args.samples < args.batch_size or args.save_every < 1 or not torch.cuda.is_available():
        raise ValueError('invalid parameters or CUDA unavailable')
    output = Path(args.checkpoint)
    if output.exists() and not args.resume:
        raise FileExistsError('checkpoint exists; use --resume')
    torch.set_num_threads(2)
    torch.manual_seed(42)
    torch.cuda.manual_seed_all(42)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    rows, rows_hash = load_rows(corpus, args.kind, args.samples, args.audio_max_seconds)
    tok_hash = hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=128)
    with open(args.init, 'rb') as f:
        source_hash = hashlib.file_digest(f, 'sha256').hexdigest()
    initial = torch.load(args.init, map_location='cpu', weights_only=True)
    if initial['settings']['tokenizer'] != tok_hash or initial['settings']['manifest_sha256'] != corpus.manifest_hash:
        raise ValueError('initial checkpoint differs from corpus or tokenizer')
    config = Config(**initial['settings']['config'])
    settings = {'config': asdict(config), 'tokenizer': tok_hash, 'manifest_sha256': corpus.manifest_hash,
                'init_sha256': source_hash, 'rows_sha256': rows_hash, 'samples': args.samples, 'batch_size': args.batch_size,
                'kind': args.kind, 'objective': 'two-pair-segment-pooled-audio-v3' if args.kind == 'speech_text' else 'two-pair-media-text-v1'}
    if args.audio_max_seconds != 12.0:
        settings['audio_max_seconds'] = args.audio_max_seconds
    if args.audio_only_adam:
        settings['optimizer'] = 'audio-only-cpu-adamw-v1'
    model = PostaMulti(config).half().cuda()
    model.load_state_dict(initial['model'])
    del initial
    if args.audio_only_adam:
        names = ('audio_patch_projection', 'temporal_cls', 'temporal_positions',
                 'temporal_modality', 'temporal_encoder', 'temporal_norm')
        for name, parameter in model.named_parameters():
            parameter.requires_grad_(any(name == prefix or name.startswith(prefix + '.') for prefix in names))
        optimizer = CPUMasterAdamW((p for p in model.parameters() if p.requires_grad), lr=0.0001)
    else:
        optimizer = CPUMasterSGD(model.parameters(), lr=0.001, loss_scale=16)
    step = 0
    if args.resume:
        state = torch.load(output, map_location='cpu', weights_only=True)
        if state.get('format') != 'posta-media-v1' or state['settings'] != settings:
            raise ValueError('media checkpoint settings differ')
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
        batch = [rows[i] for i in torch.randperm(len(rows))[:args.batch_size].tolist()]
        optimizer.zero_grad()
        queries, media = [], []
        for row in batch:
            ids = torch.tensor([tokenizer.encode(row['text']).ids], device='cuda')
            queries.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)))
            media.append(media_embedding(model, corpus, row, args.kind, config))
        loss = retrieval_loss(torch.cat(queries), torch.cat(media), torch.eye(args.batch_size, dtype=torch.bool, device='cuda'))
        if not torch.isfinite(loss):
            raise RuntimeError('non-finite loss; previous checkpoint preserved')
        optimizer.backward(loss)
        if not optimizer.step():
            raise RuntimeError('gradient overflow; previous checkpoint preserved')
        if step % args.save_every == 0 or step == args.steps:
            save_atomic(output, {'format': 'posta-media-v1', 'settings': settings, 'step': step,
                                 'model': model.state_dict(), 'optimizer': optimizer.state_dict(),
                                 'cpu_rng': torch.get_rng_state(), 'cuda_rng': torch.cuda.get_rng_state_all()})
            print(json.dumps({'saved_step': step, 'last_loss': float(loss.detach().cpu())}), flush=True)


if __name__ == '__main__':
    main()
