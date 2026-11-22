"""Paired held-out video-caption and speech-transcript research diagnostics.

Fixed small candidate pools, no claims that other candidates are irrelevant.
Weights and media stay local and no checkpoint is promoted.
"""
import argparse
import json
from pathlib import Path

import numpy as np
import torch
from tokenizers import Tokenizer

from av_alignment import _speech
from av_data import select_media_pairs
from av_metrics import paired_media_metrics
from av_real_smoke import log_mel_audio
from av_provenance import validate_av_source
from av_streaming import iter_segments_streaming
from av_temporal import aggregate_video_windows
from av_order_sensitivity import encode_reversed_video
from corpus import Corpus
from corpus_smoke import ffmpeg
from model import PostaMulti
from spec import Config, parameter_counts


def evaluate(dataset, tokenizer_path, checkpoint_path, limit=4, time_encoding=None, window_aggregation=None):
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA is required')
    if not 2 <= limit <= 16:
        raise ValueError('validation pool must contain 2-16 examples')
    import hashlib
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    corpus = Corpus(dataset)
    tokenizer_sha = hashlib.sha256(Path(tokenizer_path).read_bytes()).hexdigest()
    with Path(checkpoint_path).open('rb') as checkpoint_stream:
        checkpoint_sha = hashlib.file_digest(checkpoint_stream, 'sha256').hexdigest()
    state = torch.load(checkpoint_path, map_location='cpu', weights_only=True, mmap=True)
    settings = state.get('settings', {})
    if time_encoding is None:
        time_encoding = settings.get('video_time_encoding', 'legacy')
    if time_encoding not in ('legacy', 'relative-centered-v1'):
        raise ValueError('invalid video time encoding')
    if window_aggregation is None:
        window_aggregation = settings.get('video_window_aggregation', 'mean')
    if window_aggregation not in ('mean', 'hierarchical-v1', 'hierarchical-residual-v1'):
        raise ValueError('invalid video window aggregation')
    fmt = state.get('format')
    if fmt != 'posta-av-235m-private-pilot-v1':
        validate_av_source(state, corpus.manifest_hash, tokenizer_sha)
    elif (settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('tokenizer_sha256') != tokenizer_sha or
            settings.get('quarantined_research_only') is not True or
            settings.get('private_local_only') is not True or
            settings.get('production_promotion_allowed') is not False):
        raise ValueError('checkpoint provenance or quarantine mismatch')
    config = Config(**settings['config'])
    if parameter_counts(config)['total'] != 235179777:
        raise ValueError('unexpected model architecture')
    model = PostaMulti(config, dropout=0).half().cuda().eval()
    model.load_state_dict(state['model'], strict=True)
    del state
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    tokenizer.enable_truncation(max_length=128)
    results = {}
    with torch.inference_mode():
        for kind in ('video_text', 'speech_text'):
            rows, selection_sha = select_media_pairs(corpus, kind, 'validation', limit)
            text_vectors, media_vectors, counts = [], [], []
            reverse_distances = []
            for row in rows:
                ids = tokenizer.encode(row['text']).ids
                if len(ids) < 3 or ids[0] != 1 or ids[-1] != 2:
                    raise ValueError('invalid tokenized validation text')
                token_ids = torch.tensor([ids], device='cuda')
                text_vectors.append(model.encode_text(token_ids, torch.ones_like(token_ids, dtype=torch.bool)).squeeze(0).float())
                if kind == 'video_text':
                    parts, starts, original_frame_windows, original_time_windows = [], [], [], []
                    frame_count = 0
                    for segment in iter_segments_streaming(corpus.media_path(row), fps=30, window_seconds=2,
                                                 image_size=config.image_size):
                        frame_embeddings = []
                        for frame in segment['frames']:
                            image = torch.from_numpy(frame.copy()).permute(2, 0, 1).unsqueeze(0)
                            image = image.to(device='cuda', dtype=torch.float16).div_(255)
                            frame_embeddings.append(model.encode_image(image))
                        frames = torch.stack(frame_embeddings, dim=1)
                        timestamps = torch.tensor([segment['frame_timestamps']], device='cuda')
                        original_frame_windows.append(frames)
                        original_time_windows.append(timestamps)
                        parts.append(model.encode_video_embeddings(frames, timestamps=timestamps,
                                                                   time_encoding=time_encoding).float())
                        starts.append(timestamps[:, 0])
                        frame_count += len(segment['frames'])
                    media = aggregate_video_windows(model, torch.stack(parts, dim=1),
                                                    torch.stack(starts, dim=1), mode=window_aggregation,
                                                    time_encoding=time_encoding).squeeze(0)
                    reversed_media = encode_reversed_video(
                        model, original_frame_windows, original_time_windows,
                        time_encoding=time_encoding,
                        window_aggregation=window_aggregation).squeeze(0)
                    reverse_distances.append(float((1 - torch.nn.functional.cosine_similarity(
                        media.float().unsqueeze(0), reversed_media.float().unsqueeze(0))
                        ).clamp_(0, 2).item()))
                    counts.append(frame_count)
                else:
                    raw = ffmpeg('-i', corpus.media_path(row), '-ac', '1', '-ar', '16000', '-f', 'f32le', 'pipe:1')
                    wave = np.frombuffer(raw, dtype='<f4').copy()
                    if not 512 <= len(wave) <= 16_000 * 30:
                        raise ValueError('speech outside validation budget')
                    mel = log_mel_audio(wave, config.mel_bins).cuda().half()
                    media = _speech(model, mel, 'cuda')
                    counts.append(len(wave))
                media_vectors.append(media)
            results[kind] = {'selection_sha256': selection_sha,
                             'groups': len({x['group_id'] for x in rows}),
                             'frame_or_sample_counts': counts,
                             'metrics': paired_media_metrics(torch.stack(text_vectors), torch.stack(media_vectors))}
            if kind == 'video_text':
                results[kind]['temporal_reversal_sensitivity'] = {
                    'mean_1_minus_cosine': sum(reverse_distances) / len(reverse_distances),
                    'min_1_minus_cosine': min(reverse_distances),
                    'max_1_minus_cosine': max(reverse_distances),
                    'samples': len(reverse_distances),
                    'meaning': 'encoding changes under reversing visual content; not evidence of temporal understanding',
                }
    return {'format': 'posta-av-235m-heldout-pool-v1', 'checkpoint_format': fmt,
            'checkpoint_sha256': checkpoint_sha, 'tokenizer_sha256': tokenizer_sha,
            'manifest_sha256': corpus.manifest_hash, 'target_fps': 30,
            'video_time_encoding': time_encoding, 'video_window_aggregation': window_aggregation,
            'modalities': results,
            'limitations': ['small same-dataset candidate pools, unjudged alternatives may also be relevant',
                            'speech validation may have a single group; no general voice claim',
                            'captions are weak video labels, not audio descriptions',
                            'exact group, text and media overlap checks do not eliminate all leakage'],
            'production_promotion_allowed': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--limit', type=int, default=4)
    parser.add_argument('--time-encoding', choices=('legacy', 'relative-centered-v1'))
    parser.add_argument('--window-aggregation', choices=('mean', 'hierarchical-v1', 'hierarchical-residual-v1'))
    args = parser.parse_args()
    print(json.dumps(evaluate(args.dataset, args.tokenizer, args.checkpoint, args.limit,
                              args.time_encoding, args.window_aggregation)), flush=True)
