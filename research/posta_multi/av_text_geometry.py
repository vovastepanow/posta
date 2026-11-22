"""CPU-only diagnostic of text targets for the future AV alignment stage.

Uses isolated brief/detailed captions for the same held-out videos. This
evaluates text geometry and weak caption matching, not video understanding.
"""
import argparse
import hashlib
import json
from pathlib import Path

import torch
from tokenizers import Tokenizer

from av_metrics import paired_media_metrics
from av_provenance import validate_av_source
from corpus import Corpus
from model import PostaMulti
from spec import Config, parameter_counts
from video_caption_validation import select_validation_pairs


def run(dataset, tokenizer_path, source, limit=8):
    torch.set_num_threads(2)
    corpus = Corpus(dataset)
    token_sha = hashlib.sha256(Path(tokenizer_path).read_bytes()).hexdigest()
    pairs, selection_sha = select_validation_pairs(corpus, limit)
    checkpoint = torch.load(source, map_location='cpu', weights_only=True, mmap=True)
    settings = validate_av_source(checkpoint, corpus.manifest_hash, token_sha)
    config = Config(**settings['config'])
    if parameter_counts(config)['total'] != 235179777:
        raise ValueError('unexpected model size')
    step, source_format = checkpoint['step'], checkpoint['format']
    model = PostaMulti(config, dropout=0).eval()
    model.load_state_dict(checkpoint['model'])
    del checkpoint
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    tokenizer.enable_truncation(max_length=128)
    left, right = [], []
    with torch.inference_mode():
        for first, second in pairs:
            vectors = []
            for value in (first, second):
                tokens = tokenizer.encode(value).ids
                if len(tokens) < 3 or tokens[0] != 1 or tokens[-1] != 2:
                    raise ValueError('invalid caption tokens')
                ids = torch.tensor([tokens])
                vectors.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).squeeze(0))
            left.append(vectors[0]); right.append(vectors[1])
    return {'format': 'posta-av-text-target-geometry-v1', 'source_format': source_format,
            'source_step': step, 'text_pooling': config.text_pooling,
            'selection_sha256': selection_sha, 'metrics': paired_media_metrics(torch.stack(left), torch.stack(right)),
            'limitations': ['small weak same-video caption pool, not AV evaluation',
                            'unjudged candidate captions may also match'],
            'production_promotion_allowed': False}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--source', required=True)
    parser.add_argument('--limit', type=int, default=8)
    args = parser.parse_args()
    print(json.dumps(run(args.dataset, args.tokenizer, args.source, args.limit)), flush=True)
