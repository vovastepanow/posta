"""Evaluate held-out full utterances by pooling short audio segments."""
import argparse
import hashlib
import json
from pathlib import Path

import torch
import torch.nn.functional as F
from tokenizers import Tokenizer
from corpus import Corpus
from corpus_smoke import log_mel
from model import PostaMulti
from spec import Config
from ranking_metrics import summarize_aligned_scores


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--samples', type=int, default=4)
    p.add_argument('--split', choices=('train', 'validation'), default='validation')
    args = p.parse_args()
    if args.samples < 2 or not torch.cuda.is_available():
        raise ValueError('at least two samples and CUDA are required')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    rows = sorted(corpus.rows('speech_text', args.split), key=lambda r: (r.get('media') or {}).get('size_bytes', float('inf')))
    rows = [r for r in rows if r.get('media') and r.get('text')][:args.samples]
    if len(rows) != args.samples:
        raise ValueError('insufficient validation data')
    digest = hashlib.sha256(json.dumps([(r['text'], r['media']['sha256']) for r in rows],
                                      ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()
    state = torch.load(args.checkpoint, map_location='cpu', weights_only=True)
    settings = state['settings']
    if hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest() != settings['tokenizer']:
        raise ValueError('tokenizer mismatch')
    if corpus.manifest_hash != settings['manifest_sha256']:
        raise ValueError('dataset mismatch')
    config, step = Config(**settings['config']), state['step']
    weights = state['model']
    del state
    model = PostaMulti(config).half().cuda()
    model.load_state_dict(weights)
    del weights
    model.eval()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=128)
    text_vectors, audio_vectors = [], []
    with torch.no_grad():
        for row in rows:
            ids = torch.tensor([tokenizer.encode(row['text']).ids], device='cuda')
            text_vectors.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).float())
            mel = log_mel(corpus.media_path(row), bins=config.mel_bins, duration=None)
            chunks = []
            for start in range(0, mel.shape[-1], 256):
                chunks.append(model.encode_audio(mel[:, :, start:start+256].half().cuda()).float())
            audio_vectors.append(F.normalize(torch.stack(chunks).mean(dim=0), dim=-1))
        scores = torch.cat(text_vectors) @ torch.cat(audio_vectors).T
        metrics = summarize_aligned_scores(scores.cpu().tolist())
    print(json.dumps({'samples': len(rows), **metrics,
                      'mrr': metrics['mean_reciprocal_rank'],
                      'checkpoint_step': step, 'split': args.split, 'selection_sha256': digest,
                      'note': 'local text-to-audio ranking, ties treated conservatively; '
                              'single-speaker validation and no speech recognition accuracy'}))


if __name__ == '__main__':
    main()
