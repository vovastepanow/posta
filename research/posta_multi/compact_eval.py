"""Evaluate the from-scratch compact retriever on a fixed internal test pool.

Load only a completed compact-best checkpoint. This is post-training evaluation,
not an independent benchmark or a model-selection score; the corpus has
incomplete relevance labels and needs rights/privacy review.
"""
import argparse
import hashlib
import json
from dataclasses import asdict
from pathlib import Path

import torch
from tokenizers import Tokenizer

from compact import CompactConfig, CompactRetriever
from compact_train import encode_pairs, validate
from compare_checkpoints import atomic_report
from corpus import Corpus
from retrieval_data_quality import curate_retrieval_pairs


def check_provenance(checkpoint, manifest_sha256, tokenizer_sha256):
    if checkpoint.get('format') != 'posta-compact-best-v1':
        raise ValueError('expected a completed compact best checkpoint')
    settings = checkpoint.get('settings')
    if not isinstance(settings, dict):
        raise ValueError('checkpoint settings missing')
    if (settings.get('dataset_sha256') != manifest_sha256 or
            settings.get('tokenizer_sha256') != tokenizer_sha256):
        raise ValueError('checkpoint dataset or tokenizer provenance differs')
    try:
        config = CompactConfig(**settings['config'])
    except (KeyError, TypeError) as error:
        raise ValueError('checkpoint config missing or incompatible') from error
    if not isinstance(checkpoint.get('step'), int) or checkpoint['step'] <= 0:
        raise ValueError('checkpoint step missing')
    return config


def run(checkpoint_path, dataset_path, tokenizer_path, output_path, *,
        samples=64, batch_size=8, cpu=False):
    if samples < 2 or batch_size < 1:
        raise ValueError('invalid test parameters')
    output = Path(output_path)
    if output.exists():
        raise FileExistsError('test result exists; do not overwrite or repeatedly tune against test')
    corpus = Corpus(dataset_path)
    tokenizer_path = Path(tokenizer_path)
    tokenizer_sha = hashlib.sha256(tokenizer_path.read_bytes()).hexdigest()
    state = torch.load(checkpoint_path, map_location='cpu', weights_only=True)
    config = check_provenance(state, corpus.manifest_hash, tokenizer_sha)
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    if tokenizer.get_vocab_size() != config.vocab_size:
        raise ValueError('tokenizer vocabulary differs from checkpoint')
    pairs, selection_hash, _ = curate_retrieval_pairs(
        corpus, samples, split='test', avoid_heldout=False)
    encoded = encode_pairs(tokenizer, pairs, config.max_length)
    device = torch.device('cpu' if cpu or not torch.cuda.is_available() else 'cuda')
    torch.set_num_threads(2)
    if device.type == 'cuda':
        torch.cuda.set_per_process_memory_fraction(0.65)
    model = CompactRetriever(config).float().to(device)
    model.load_state_dict(state['model'])
    model.eval()
    metrics = validate(model, encoded, device, batch_size)
    result = {
        'format': 'posta-compact-internal-test-v1',
        'checkpoint_step': state['step'],
        'checkpoint': Path(checkpoint_path).name,
        'manifest_sha256': corpus.manifest_hash,
        'tokenizer_sha256': tokenizer_sha,
        'selected_pairs_sha256': selection_hash,
        'config': asdict(config),
        'test_metrics': metrics,
        'limitations': [
            'Internal test split only, not independent production relevance labels',
            'The score should not be used for repeated hyperparameter tuning',
            'Exact split checks cannot rule out near-duplicates or translated overlap',
            'Other-query positives may be unlabeled but relevant to the query',
        ],
    }
    atomic_report(output, result)
    return result


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--output', required=True)
    p.add_argument('--samples', type=int, default=64)
    p.add_argument('--batch-size', type=int, default=8)
    p.add_argument('--cpu', action='store_true')
    args = p.parse_args()
    result = run(args.checkpoint, args.dataset, args.tokenizer, args.output,
                 samples=args.samples, batch_size=args.batch_size, cpu=args.cpu)
    print(json.dumps({'checkpoint': result['checkpoint'],
                      'metrics': result['test_metrics'],
                      'output': args.output}, ensure_ascii=False))


if __name__ == '__main__':
    main()
