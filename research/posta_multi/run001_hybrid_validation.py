"""Calibrate rank fusion on local validation queries and test on disjoint queries.

All queries share one fixed candidate pool. The 64-query calibration prefix
must be exactly the checkpoint-selection sample; the remainder is separate
from checkpoint selection but still internal validation, not a final test.
No corpus content or embeddings are written to the report.
"""
import argparse
from collections import defaultdict
import hashlib
import json
from pathlib import Path

import torch
from tokenizers import Tokenizer

from bm25_baseline import bm25_scores, words
from compare_checkpoints import atomic_report
from corpus import Corpus
from frozen_nanomiracl_eval import file_digest
from hybrid_retrieval import compare_splits, evaluate_rows, fuse_scores
from model import PostaMulti
from retrieval_data_quality import curate_retrieval_pairs, normalized
from run001_retrieval import STAGE_FORMAT, _encode_one, encode_triples
from run001_lexical_validation import CharacterBM25
from run001_smoke import RUN_ID
from spec import Config, parameter_counts


def language_metrics(corpus, pairs, rows, lexical_rows, calibration, semantic_weight):
    if len(pairs) != len(rows) or len(rows) != len(lexical_rows) or not 0 <= calibration < len(rows):
        raise ValueError('language metric rows must be aligned')
    language_by_query = defaultdict(set)
    heldout_queries = {normalized(q) for q, _, _ in pairs[calibration:]}
    for row in corpus.rows('retrieval', 'validation'):
        query = normalized(row.get('text'))
        if query in heldout_queries:
            language = row.get('language')
            language_by_query[query].add(language if isinstance(language, str) and language else 'und')
    groups = defaultdict(lambda: ([], []))
    for (query, _, _), row, lexrow in zip(
            pairs[calibration:], rows[calibration:], lexical_rows[calibration:]):
        languages = language_by_query.get(normalized(query), set())
        label = next(iter(languages)) if len(languages) == 1 else 'mixed_or_unknown'
        groups[label][0].append(row)
        groups[label][1].append(lexrow)
    return {
        language: {'word_bm25': evaluate_rows(lexrows, 0.0),
                   'lexical_fusion': evaluate_rows(subrows, 0.0),
                   'semantic': evaluate_rows(subrows, 1.0),
                   'selected_hybrid': evaluate_rows(subrows, semantic_weight)}
        for language, (subrows, lexrows) in sorted(groups.items())}


def evaluate(dataset, tokenizer_path, checkpoint_path, samples=128, calibration=64):
    if not torch.cuda.is_available():
        raise RuntimeError('hybrid comparison requires CUDA')
    if calibration != 64 or samples <= calibration:
        raise ValueError('require the fixed 64-query calibration prefix and additional validation')
    corpus = Corpus(dataset)
    pairs, pairs_sha, _ = curate_retrieval_pairs(corpus, samples, split='validation', avoid_heldout=False)
    prefix, prefix_sha, _ = curate_retrieval_pairs(corpus, calibration, split='validation', avoid_heldout=False)
    if pairs[:calibration] != prefix:
        raise ValueError('validation prefix changed')
    tokenizer_path = Path(tokenizer_path)
    tokenizer_sha = hashlib.sha256(tokenizer_path.read_bytes()).hexdigest()
    state = torch.load(checkpoint_path, map_location='cpu', weights_only=True)
    settings = state.get('settings', {})
    if (state.get('format') != STAGE_FORMAT + '-best' or
            settings.get('run_id') != RUN_ID or
            settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('tokenizer_sha256') != tokenizer_sha or
            settings.get('val_pairs') != calibration or
            settings.get('val_pairs_sha256') != prefix_sha or
            type(state.get('step')) is not int or state['step'] < 1):
        raise ValueError('checkpoint, dataset, tokenizer or calibration provenance mismatch')
    config = Config(**settings['config'])
    if config.text_pooling != 'mean-residual' or parameter_counts(config)['total'] != 235179777:
        raise ValueError('checkpoint is not the selected retrieval architecture')
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    if any(tokenizer.token_to_id(token) != value for token, value in
           (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2))):
        raise ValueError('tokenizer special IDs mismatch')
    encoded = encode_triples(tokenizer, pairs, settings['max_length'])
    docs = list(dict.fromkeys(value for _, pos, neg in pairs for value in (pos, neg)))
    token_docs = {}
    for (_, positive, negative), (_, pos_ids, neg_ids) in zip(pairs, encoded):
        for text, ids in ((positive, pos_ids), (negative, neg_ids)):
            token_docs[text] = ids
    if len(set(token_docs.values())) != len(docs):
        raise ValueError('tokenization collapsed distinct candidates')
    doc_index = {doc: i for i, doc in enumerate(docs)}
    indexed = [words(doc) for doc in docs]
    character = CharacterBM25(docs)
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    model = PostaMulti(config, dropout=0).half().cuda()
    model.load_state_dict(state['model'])
    model.eval()
    with torch.no_grad():
        doc_vectors = torch.stack([_encode_one(model, token_docs[doc], 'cuda').float().cpu() for doc in docs])
        rows, lexical_rows = [], []
        for (query, positive, negative), (query_ids, _, _) in zip(pairs, encoded):
            query_vector = _encode_one(model, query_ids, 'cuda').float().cpu()
            word_scores, char_scores = bm25_scores(query, indexed), character.scores(query)
            lexical_rows.append({'lexical': word_scores, 'semantic': char_scores,
                                 'positive': doc_index[positive], 'negative': doc_index[negative]})
            rows.append({'lexical': fuse_scores(word_scores, char_scores, semantic_weight=0.5),
                         'semantic': (doc_vectors @ query_vector).tolist(),
                         'positive': doc_index[positive], 'negative': doc_index[negative]})
    result = compare_splits(rows, calibration)
    result['evaluation']['lexical_fusion'] = result['evaluation'].pop('bm25')
    result['evaluation']['word_bm25'] = evaluate_rows(lexical_rows[calibration:], 0.0)
    result['evaluation']['character_bm25'] = evaluate_rows(lexical_rows[calibration:], 1.0)
    result['evaluation_by_language'] = language_metrics(
        corpus, pairs, rows, lexical_rows, calibration, result['selected_semantic_weight'])
    return {'format': 'posta-run001-hybrid-internal-validation-v1',
            'manifest_sha256': corpus.manifest_hash, 'tokenizer_sha256': tokenizer_sha,
            'checkpoint_sha256': file_digest(checkpoint_path), 'checkpoint_step': state['step'],
            'validation_pairs_sha256': pairs_sha, 'calibration_pairs_sha256': prefix_sha,
            'candidate_pool_size': len(docs), 'rank_constant': 60,
            'lexical_char_weight': 0.5,
            'quarantined_research_only': settings.get('quarantined_research_only', False),
            'production_promotion_allowed': False, **result,
            'limitations': ['internal validation only, not an independent benchmark',
                            'candidate pool contains documents from both query groups',
                            'BM25 uses full text; the encoder truncates',
                            'one known positive per query; unjudged candidates may be relevant']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError('hybrid report already exists')
    result = evaluate(args.dataset, args.tokenizer, args.checkpoint)
    atomic_report(args.output, result)
    print(json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
