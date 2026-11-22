"""One private CPU optimizer step on real video and speech using a tiny model.

This is a plumbing test only: no 235M weights, quality claim, output artifact,
source export, model promotion, or assertion that captions describe sound.
"""
import argparse
from dataclasses import replace
import json
import time

import torch

from av_alignment import alignment_backward, configure_av_trainable
from av_data import select_media_pairs
from av_real_smoke import log_mel_audio
from av_streaming import iter_segments_streaming
from corpus import Corpus
from corpus_smoke import ffmpeg
from model import PostaMulti
from synthetic_train import tiny_config
from tokenizer import encode
import numpy as np


def run(dataset):
    torch.set_num_threads(2)
    torch.manual_seed(771)
    corpus = Corpus(dataset)
    videos, video_sha = select_media_pairs(corpus, 'video_text', 'train', 2)
    speeches, speech_sha = select_media_pairs(corpus, 'speech_text', 'train', 2)
    config = replace(tiny_config(), temporal_length=256)
    model = PostaMulti(config, dropout=0)
    configure_av_trainable(model)
    optimizer = torch.optim.SGD((p for p in model.parameters() if p.requires_grad), lr=0.001)
    started = time.monotonic()
    video_pairs, speech_pairs = [], []
    for row in videos:
        ids = torch.tensor([encode(row['text'], config.text_length)])
        with torch.no_grad():
            text = model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).squeeze(0)
        windows = []
        for segment in iter_segments_streaming(corpus.media_path(row), fps=30, window_seconds=2,
                                     image_size=config.image_size):
            frames = torch.from_numpy(segment['frames']).permute(0, 3, 1, 2).unsqueeze(0).float() / 255
            times = torch.tensor([segment['frame_timestamps']], dtype=torch.float32)
            windows.append((frames, times))
        video_pairs.append((text, windows))
    for row in speeches:
        ids = torch.tensor([encode(row['text'], config.text_length)])
        with torch.no_grad():
            text = model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).squeeze(0)
        samples = ffmpeg('-i', corpus.media_path(row), '-ac', '1', '-ar', '16000', '-f', 'f32le', 'pipe:1')
        wave = np.frombuffer(samples, dtype='<f4').copy()
        if not 512 <= len(wave) <= 16_000 * 30:
            raise ValueError('speech length outside smoke-test budget')
        speech_pairs.append((text, log_mel_audio(wave, config.mel_bins)))
    before_image = model.image_patches.weight.detach().clone()
    before_audio = model.audio_patch_projection.weight.detach().clone()
    optimizer.zero_grad(set_to_none=True)
    loss, stats = alignment_backward(model, video_pairs, speech_pairs, 'cpu',
                                     time_encoding='relative-centered-v1',
                                     window_aggregation='hierarchical-v1')
    optimizer.step()
    image_change = float((model.image_patches.weight - before_image).abs().max())
    audio_change = float((model.audio_patch_projection.weight - before_audio).abs().max())
    if image_change <= 0 or audio_change <= 0:
        raise RuntimeError('one of the media paths was not updated')
    return {'format': 'posta-av-real-tiny-training-smoke-v1', 'manifest_sha256': corpus.manifest_hash,
            'video_selection_sha256': video_sha, 'speech_selection_sha256': speech_sha,
            'video_samples': len(video_pairs), 'video_frames': [sum(x[0].shape[1] for x in windows)
                                                               for _, windows in video_pairs],
            'speech_samples': len(speech_pairs), 'target_fps': 30,
            'video_time_encoding': 'relative-centered-v1', 'optimizer_steps': 1,
            'video_window_aggregation': 'hierarchical-v1',
            'loss': loss, 'metrics': stats, 'image_weight_max_change': image_change,
            'audio_weight_max_change': audio_change, 'elapsed_seconds': round(time.monotonic()-started, 2),
            'model': 'tiny random CPU model', 'trained_235m': False, 'quality_verified': False,
            'production_promotion_allowed': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    args = parser.parse_args()
    print(json.dumps(run(args.dataset)), flush=True)
