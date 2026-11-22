"""Decode a complete existing video with audio and exercise a tiny random model.

Read-only private research. Reports aggregate dimensions/timing only, never
raw media or descriptions. This is an integration probe, not training or a
quality/throughput estimate for the 235M model.
"""
import argparse
from dataclasses import replace
import hashlib
import json
import time

import numpy as np
import torch

from av_segments import probe
from av_streaming import iter_segments_streaming
from corpus import Corpus
from model import PostaMulti
from synthetic_train import tiny_config


def log_mel_audio(samples, bins):
    wave = torch.from_numpy(np.asarray(samples, dtype=np.float32).copy())
    if wave.numel() < 512:
        raise ValueError('audio window is too short')
    power = torch.stft(wave, n_fft=400, hop_length=160, window=torch.hann_window(400),
                       return_complex=True).abs().square()
    hz_to_mel = lambda hz: 2595 * torch.log10(1 + hz / 700)
    mel_to_hz = lambda mel: 700 * (10 ** (mel / 2595) - 1)
    points = mel_to_hz(torch.linspace(hz_to_mel(torch.tensor(0.)),
                                     hz_to_mel(torch.tensor(8000.)), bins + 2))
    frequencies = torch.linspace(0, 8000, power.shape[0])
    left = (frequencies[None, :] - points[:-2, None]) / (points[1:-1, None] - points[:-2, None])
    right = (points[2:, None] - frequencies[None, :]) / (points[2:, None] - points[1:-1, None])
    bank = torch.minimum(left, right).clamp(min=0)
    return (bank @ power).clamp_min(1e-10).log().unsqueeze(0)


def select_real_video(corpus):
    candidates = sorted(corpus.rows('video_text', 'train'),
        key=lambda row: hashlib.sha256(row['record_id'].encode()).digest())
    chosen, metadata = None, None
    for row in candidates:
        try:
            duration = float(row.get('duration_seconds') or 0)
        except (TypeError, ValueError):
            continue
        if not 3 <= duration <= 6:
            continue
        path = corpus.media_path(row)
        info = probe(path)
        if info['has_audio_stream'] and 3 <= info['duration_seconds'] <= 6:
            chosen, metadata = path, info
            break
    if chosen is None:
        raise ValueError('no suitable local training video with audio')
    return chosen, metadata


def run(dataset, fps=30, window_seconds=2):
    corpus = Corpus(dataset)
    chosen, metadata = select_real_video(corpus)
    config = replace(tiny_config(), temporal_length=256)
    torch.set_num_threads(2)
    torch.manual_seed(4321)
    model = PostaMulti(config, dropout=0).eval()
    started = time.monotonic()
    outputs = []
    with torch.no_grad():
        for segment in iter_segments_streaming(chosen, fps=fps, window_seconds=window_seconds,
                                     image_size=config.image_size):
            frames = torch.from_numpy(segment['frames']).permute(0, 3, 1, 2).unsqueeze(0).float() / 255
            times = torch.tensor([segment['frame_timestamps']], dtype=torch.float32)
            video = model.encode_video(frames, timestamps=times)
            mel = log_mel_audio(segment['audio'], config.mel_bins)
            audio = model.encode_audio(mel)
            slots = torch.stack((torch.zeros_like(video), torch.zeros_like(video), video, audio), dim=1)
            fused = model.fuse_post(slots, torch.tensor([[False, False, True, True]]))
            if not all(bool(torch.isfinite(x).all()) for x in (video, audio, fused)):
                raise RuntimeError('nonfinite AV output')
            outputs.append({'start_seconds': round(segment['start'], 4),
                            'end_seconds': round(segment['end'], 4),
                            'video_frames': len(segment['frames']),
                            'audio_samples': len(segment['audio']),
                            'first_frame_seconds': round(segment['frame_timestamps'][0], 4),
                            'last_frame_seconds': round(segment['frame_timestamps'][-1], 4)})
    return {'format': 'posta-av-tiny-real-media-smoke-v1',
            'manifest_sha256': corpus.manifest_hash,
            'source_duration_seconds': metadata['duration_seconds'],
            'source_fps': metadata['source_fps'], 'target_fps': fps,
            'window_seconds': window_seconds, 'windows': outputs,
            'elapsed_seconds': round(time.monotonic() - started, 3),
            'model': 'tiny randomly initialized CPU model', 'trained': False,
            'real_235m_tested': False, 'quality_verified': False,
            'limitations': ['output frame timestamps are resampled PTS, not original frame PTS',
                            'no audiovisual synchronization accuracy or model quality asserted']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--fps', type=int, default=30)
    parser.add_argument('--window-seconds', type=float, default=2)
    args = parser.parse_args()
    print(json.dumps(run(args.dataset, args.fps, args.window_seconds), ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
