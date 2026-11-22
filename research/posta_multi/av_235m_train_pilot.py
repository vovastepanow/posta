"""One bounded private AV alignment step from a quarantined text checkpoint.

This is a feasibility pilot, not a quality claim or releasable model. The
video-caption and speech-transcript objectives are separate; fusion is frozen.
The two videos are consumed in full at 30 fps, in short windows. This script
does not overwrite a checkpoint and does not resume or promote the output.
"""
import argparse
from dataclasses import asdict
import hashlib
import json
import time
from pathlib import Path

import numpy as np
import torch
from tokenizers import Tokenizer

from av_alignment import alignment_backward, configure_av_trainable
from av_data import select_media_pairs
from av_real_smoke import log_mel_audio
from av_provenance import validate_av_source
from av_streaming import iter_segments_streaming
from corpus import Corpus
from corpus_smoke import ffmpeg
from cpu_master_adam import CPUMasterAdamW
from model import PostaMulti
from spec import Config, parameter_counts
from train_text import save_atomic


def run(dataset, tokenizer_path, source_path, output_path, window_aggregation='hierarchical-v1'):
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA is required')
    if window_aggregation not in ('mean', 'hierarchical-v1', 'hierarchical-residual-v1'):
        raise ValueError('invalid video window aggregation')
    output_path = Path(output_path)
    if output_path.exists():
        raise FileExistsError('pilot output already exists')
    with open(source_path, 'rb') as source:
        source_sha256 = hashlib.file_digest(source, 'sha256').hexdigest()
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    corpus = Corpus(dataset)
    tokenizer_path = Path(tokenizer_path)
    tokenizer_sha = hashlib.sha256(tokenizer_path.read_bytes()).hexdigest()
    state = torch.load(source_path, weights_only=True, map_location='cpu', mmap=True)
    settings = validate_av_source(state, corpus.manifest_hash, tokenizer_sha)
    config = Config(**settings['config'])
    if parameter_counts(config)['total'] != 235179777:
        raise ValueError('unexpected model architecture')
    source_step = state['step']
    source_format = state['format']
    model = PostaMulti(config, dropout=0).half().cuda()
    model.load_state_dict(state['model'], strict=True)
    del state
    configure_av_trainable(model)
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    tokenizer.enable_truncation(max_length=128)
    videos, video_sha = select_media_pairs(corpus, 'video_text', 'train', 2)
    speeches, speech_sha = select_media_pairs(corpus, 'speech_text', 'train', 2)
    def text_target(row):
        tokens = tokenizer.encode(row['text']).ids
        if len(tokens) < 3 or tokens[0] != 1 or tokens[-1] != 2:
            raise ValueError('invalid text token sequence')
        ids = torch.tensor([tokens], device='cuda')
        with torch.no_grad():
            return model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).squeeze(0).float()
    video_pairs, speech_pairs = [], []
    for row in videos:
        windows = []
        for part in iter_segments_streaming(corpus.media_path(row), fps=30, window_seconds=2,
                                  image_size=config.image_size):
            frames = torch.from_numpy(part['frames']).permute(0, 3, 1, 2).unsqueeze(0).float() / 255
            times = torch.tensor([part['frame_timestamps']], dtype=torch.float32)
            windows.append((frames, times))
        video_pairs.append((text_target(row), windows))
    for row in speeches:
        raw = ffmpeg('-i', corpus.media_path(row), '-ac', '1', '-ar', '16000', '-f', 'f32le', 'pipe:1')
        wave = np.frombuffer(raw, dtype='<f4').copy()
        if not 512 <= len(wave) <= 16_000 * 30:
            raise ValueError('speech outside pilot duration budget')
        speech_pairs.append((text_target(row), log_mel_audio(wave, config.mel_bins)))
    optimizer = CPUMasterAdamW((p for p in model.parameters() if p.requires_grad),
                               lr=0.00001, loss_scale=16, max_grad_norm=1.0)
    optimizer.zero_grad()
    torch.cuda.reset_peak_memory_stats()
    started = time.monotonic()
    loss, metrics = alignment_backward(model, video_pairs, speech_pairs, 'cuda', optimizer,
                                       time_encoding='relative-centered-v1',
                                       window_aggregation=window_aggregation)
    if not optimizer.step():
        raise RuntimeError('nonfinite or missing gradients; no checkpoint saved')
    elapsed = time.monotonic() - started
    summary = {'format': 'posta-av-235m-private-pilot-v1', 'source_step': source_step,
               'source_format': source_format,
               'source_checkpoint_sha256': source_sha256,
               'manifest_sha256': corpus.manifest_hash, 'tokenizer_sha256': tokenizer_sha,
               'config': asdict(config), 'video_selection_sha256': video_sha,
               'speech_selection_sha256': speech_sha, 'video_examples': 2, 'speech_examples': 2,
               'video_frame_counts': [sum(frames.shape[1] for frames, _ in windows)
                                      for _, windows in video_pairs],
               'target_fps': 30, 'video_time_encoding': 'relative-centered-v1',
               'video_window_aggregation': window_aggregation,
               'step': 1, 'loss': loss, 'metrics': metrics,
               'gradient_norm': optimizer.last_grad_norm, 'elapsed_seconds': round(elapsed, 3),
               'cuda_peak_allocated_mib': round(torch.cuda.max_memory_allocated()/2**20, 1),
               'quarantined_research_only': True, 'private_local_only': True,
               'production_promotion_allowed': False, 'quality_verified': False,
               'limitations': ['one step and two pairs per modality; no held-out quality evaluation',
                               'video captions are weak labels; speech transcripts cover speech only',
                               'fusion weights remain untrained']}
    save_atomic(output_path, {'format': summary['format'], 'settings': summary, 'model': model.state_dict()})
    # Hash the finished checkpoint after atomic save. The digest belongs in
    # the external run summary, not inside the hashed checkpoint itself.
    with output_path.open('rb') as checkpoint_stream:
        summary['output_checkpoint_sha256'] = hashlib.file_digest(checkpoint_stream, 'sha256').hexdigest()
    return summary


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--source', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--window-aggregation', default='hierarchical-v1',
                        choices=('mean', 'hierarchical-v1', 'hierarchical-residual-v1'))
    args = parser.parse_args()
    print(json.dumps(run(args.dataset, args.tokenizer, args.source, args.output,
                         args.window_aggregation)), flush=True)
