"""Small held-out retrieval check: pairs and exploratory full-candidate ranking.

The pool consists of unique positive/negative passages from sampled validation
rows. It is NOT a fully labeled retrieval benchmark: documents labeled relevant
to other queries can also be relevant to this query.
"""
import argparse
import hashlib
import json
from pathlib import Path

import torch
from tokenizers import Tokenizer

from corpus import Corpus
from model import PostaMulti
from ranking_metrics import conservative_rank, summarize_ranks
from retrieval_stage import load_pairs, load_pairs_sampled
from spec import Config


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--samples', type=int, default=16)
    p.add_argument('--split', choices=('validation', 'test'), default='validation')
    p.add_argument('--sample-hash', action='store_true', help='choose split rows via stable hashes, independent of JSONL ordering')
    args = p.parse_args()
    if args.samples < 1:
        raise ValueError('samples must be positive')
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA required')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    pairs, digest = (load_pairs_sampled if args.sample_hash else load_pairs)(corpus, args.samples, split=args.split)
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

    # Retain legacy one-negative accuracy while also evaluating each query
    # against a shared, deduplicated pool of the sampled validation passages.
    documents = list(dict.fromkeys(text for _, positive, negative in pairs
                                   for text in (positive, negative)))
    doc_index = {text: i for i, text in enumerate(documents)}
    queries = list(dict.fromkeys(query for query, _, _ in pairs))

    def embed(text):
        ids = torch.tensor([tokenizer.encode(text).ids], dtype=torch.long, device='cuda')
        return model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).float().squeeze(0).cpu()

    with torch.no_grad():
        # CPU storage keeps the candidate vectors outside the limited 3-GB GPU.
        candidate_vectors = torch.stack([embed(document) for document in documents])
        query_vectors = {query: embed(query) for query in queries}

    wins = ties = 0
    ranks = []
    for query, positive, negative in pairs:
        scores = torch.mv(candidate_vectors, query_vectors[query]).tolist()
        p_score = scores[doc_index[positive]]
        n_score = scores[doc_index[negative]]
        wins += p_score > n_score
        ties += p_score == n_score
        ranks.append(conservative_rank(scores, doc_index[positive]))
    result = {
        'samples': len(pairs),
        'positive_above_negative': wins,
        'ties': ties,
        'pair_accuracy': round(wins / len(pairs), 4),
        'candidate_pool_size': len(documents),
        'unique_queries': len(queries),
        **summarize_ranks(ranks),
        'checkpoint_step': step,
        'validation_pairs_sha256': digest,
        'split': args.split,
        'sample_mode': 'stable-hash' if args.sample_hash else 'head',
        'note': ('Exploratory ranking over sampled local held-out positives/negatives; '
                 'other queries may have relevant unlabeled passages. '
                 'Not independent benchmark quality or production top-k recall.'),
    }
    print(json.dumps(result))


if __name__ == '__main__':
    main()
