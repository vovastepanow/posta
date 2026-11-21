"""Small held-out positive-vs-explicit-negative retrieval check."""
import argparse
import hashlib
import json
from pathlib import Path
import torch
from tokenizers import Tokenizer
from corpus import Corpus
from model import PostaMulti
from retrieval_stage import load_pairs
from spec import Config


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--samples', type=int, default=16)
    args = p.parse_args()
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA required')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    pairs, digest = load_pairs(corpus, args.samples, split='validation')
    state = torch.load(args.checkpoint, map_location='cpu', weights_only=True)
    settings = state['settings']
    if hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest() != settings['tokenizer']:
        raise ValueError('tokenizer hash mismatch')
    if corpus.manifest_hash != settings['manifest_sha256']:
        raise ValueError('corpus hash mismatch')
    config = Config(**settings['config'])
    step = state['step']
    weights = state['model']
    del state
    model = PostaMulti(config).half().cuda()
    model.load_state_dict(weights)
    del weights
    model.eval()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=128)
    wins = ties = 0
    with torch.no_grad():
        for pair in pairs:
            vectors = []
            for text in pair:
                ids = torch.tensor([tokenizer.encode(text).ids], dtype=torch.long, device='cuda')
                vectors.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).float())
            positive = float((vectors[0] * vectors[1]).sum())
            negative = float((vectors[0] * vectors[2]).sum())
            wins += positive > negative
            ties += positive == negative
    print(json.dumps({'samples': len(pairs), 'positive_above_negative': wins, 'ties': ties,
                      'pair_accuracy': wins / len(pairs), 'checkpoint_step': step,
                      'validation_pairs_sha256': digest,
                      'note': 'local validation, one upstream negative per query; not top-k retrieval'}))


if __name__ == '__main__':
    main()
