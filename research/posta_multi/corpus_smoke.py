"""Small CPU integration test of all dataset types; no checkpoint is saved."""
import argparse
import io
import json
import subprocess

import numpy as np
from PIL import Image
import torch
from dataclasses import replace

from corpus import Corpus, KINDS
from model import PostaMulti
from synthetic_train import tiny_config
from tokenizer import encode


def rgb(image, size):
    image = image.convert('RGB').resize((size, size), Image.Resampling.BICUBIC)
    arr = np.asarray(image, dtype=np.float32).copy() / 255.0
    return torch.from_numpy(arr).permute(2, 0, 1).unsqueeze(0)


def ffmpeg(*args):
    return subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', *map(str, args)],
                          check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30).stdout


def log_mel(path, bins=16, duration=0.25):
    limit = [] if duration is None else ['-t', str(duration)]
    data = ffmpeg('-i', path, *limit, '-ac', '1', '-ar', '16000', '-f', 'f32le', 'pipe:1')
    wave = torch.from_numpy(np.frombuffer(data, dtype='<f4').copy())
    if wave.numel() < 512:
        raise ValueError('audio too short')
    power = torch.stft(wave, n_fft=400, hop_length=160, window=torch.hann_window(400),
                       return_complex=True).abs().square()
    hz_to_mel = lambda hz: 2595 * torch.log10(1 + hz / 700)
    mel_to_hz = lambda mel: 700 * (10 ** (mel / 2595) - 1)
    points = mel_to_hz(torch.linspace(hz_to_mel(torch.tensor(0.)), hz_to_mel(torch.tensor(8000.)), bins + 2))
    frequencies = torch.linspace(0, 8000, power.shape[0])
    left = (frequencies[None, :] - points[:-2, None]) / (points[1:-1, None] - points[:-2, None])
    right = (points[2:, None] - frequencies[None, :]) / (points[2:, None] - points[1:-1, None])
    bank = torch.minimum(left, right).clamp(min=0)
    return (bank @ power).clamp_min(1e-10).log().unsqueeze(0)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    args = p.parse_args()
    torch.set_num_threads(2)
    torch.manual_seed(42)
    corpus = Corpus(args.dataset)
    config = replace(tiny_config(), vocab_size=260)
    model = PostaMulti(config, dropout=0).eval()
    results = {}
    with torch.no_grad():
        for kind in KINDS:
            row = next(corpus.rows(kind))
            token_ids = torch.tensor([encode(row['text'], config.text_length)])
            embedding = model.encode_text(token_ids, torch.ones_like(token_ids, dtype=torch.bool))
            if kind == 'retrieval':
                positive = torch.tensor([encode(row['positive'], config.text_length)])
                model.encode_text(positive, torch.ones_like(positive, dtype=torch.bool))
            if kind in ('image_text', 'meme_text', 'ocr_text'):
                with Image.open(corpus.media_path(row)) as image:
                    embedding = model.encode_image(rgb(image, config.image_size))
            if kind == 'video_text':
                path = corpus.media_path(row)
                data = ffmpeg('-i', path, '-frames:v', '1', '-f', 'image2pipe', '-vcodec', 'png', 'pipe:1')
                with Image.open(io.BytesIO(data)) as image:
                    embedding = model.encode_video(rgb(image, config.image_size).unsqueeze(1))
            if kind == 'speech_text':
                embedding = model.encode_audio(log_mel(corpus.media_path(row)))
            if not torch.isfinite(embedding).all():
                raise ValueError(f'non-finite output for {kind}')
            results[kind] = {'source': row['dataset_id'], 'revision': row['dataset_revision'],
                             'embedding_dim': embedding.shape[-1]}
    print(json.dumps({'manifest_sha256': corpus.manifest_hash, 'examples': results,
                      'model': 'tiny random CPU', 'trained': False}, ensure_ascii=False))


if __name__ == '__main__':
    main()
