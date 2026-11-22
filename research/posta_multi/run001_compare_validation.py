"""Compare a Run 001 retriever and BM25 on the same local validation pairs.

This is development validation, not independent testing. The baseline ranks
full text while the model uses its checkpoint's token length. Outputs contain
aggregate scores and provenance, not query or document texts.
"""
import argparse
import hashlib
import json
from pathlib import Path

import torch
from tokenizers import Tokenizer

from bm25_baseline import score_pairs
from compare_checkpoints import atomic_report
from corpus import Corpus
from frozen_nanomiracl_eval import file_digest
from model import PostaMulti
from retrieval_data_quality import curate_retrieval_pairs
from run001_retrieval import STAGE_FORMAT, encode_triples, evaluate_retrieval
from run001_smoke import RUN_ID
from spec import Config, parameter_counts


def compare(dataset, tokenizer_path, checkpoint_path, limit):
    if not torch.cuda.is_available():
        raise RuntimeError('comparison requires CUDA')
    corpus = Corpus(dataset)
    pairs, pair_sha, _ = curate_retrieval_pairs(
        corpus, limit, split='validation', avoid_heldout=False)
    tokenizer_path = Path(tokenizer_path)
    tokenizer_sha = hashlib.sha256(tokenizer_path.read_bytes()).hexdigest()
    state = torch.load(checkpoint_path, map_location='cpu', weights_only=True)
    settings = state.get('settings', {})
    if (state.get('format') != STAGE_FORMAT + '-best' or
            settings.get('run_id') != RUN_ID or
            settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('tokenizer_sha256') != tokenizer_sha or
            type(state.get('step')) is not int or state['step'] < 1):
        raise ValueError('checkpoint does not match corpus and tokenizer')
    config = Config(**settings['config'])
    if config.text_pooling != 'mean-residual' or parameter_counts(config)['total'] != 235179777:
        raise ValueError('checkpoint is not the Run 001 retrieval architecture')
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    for token, expected in (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2)):
        if tokenizer.token_to_id(token) != expected:
            raise ValueError('tokenizer special IDs mismatch')
    encoded = encode_triples(tokenizer, pairs, settings['max_length'])
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    model = PostaMulti(config, dropout=0).half().cuda()
    model.load_state_dict(state['model'])
    model.eval()
    model_metrics = evaluate_retrieval(model, encoded, 'cuda')
    baseline = score_pairs(pairs, pair_sha)
    if model_metrics['candidate_pool_size'] != baseline['candidate_pool_size']:
        raise ValueError('tokenization changed candidate count; comparison is not aligned')
    return {'format': 'posta-run001-local-validation-comparison-v1',
            'manifest_sha256': corpus.manifest_hash, 'tokenizer_sha256': tokenizer_sha,
            'validation_pairs_sha256': pair_sha, 'checkpoint_step': state['step'],
            'checkpoint_sha256': file_digest(checkpoint_path),
            'checkpoint_training_validation_pairs': settings['val_pairs'],
            'quarantined_research_only': settings.get('quarantined_research_only', False),
            'model': model_metrics, 'bm25': baseline,
            'limitations': ['internal validation is not independent test data',
                            'the sample includes pairs used for checkpoint selection',
                            'BM25 uses full text; the encoder truncates to its token budget',
                            'unjudged documents can be relevant'],
            'production_promotion_allowed': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--samples', type=int, default=128)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError('comparison report already exists')
    if args.samples < 16:
        raise ValueError('at least 16 validation pairs required')
    result = compare(args.dataset, args.tokenizer, args.checkpoint, args.samples)
    atomic_report(args.output, result)
    print(json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
