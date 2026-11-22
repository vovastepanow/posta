"""Weak English validation: retrieve a video's detailed caption from its brief caption.

This is not a real search-query benchmark or a video-encoder evaluation. It
uses only the local validation split and screens exact train-text/video overlap.
No texts, media or embeddings are exported.
"""
import argparse
import hashlib
import json
from pathlib import Path

import torch
from tokenizers import Tokenizer

from bm25_baseline import bm25_scores, words
from compare_checkpoints import atomic_report
from corpus import Corpus, KINDS
from frozen_nanomiracl_eval import file_digest
from hybrid_retrieval import fuse_scores
from model import PostaMulti
from ranking_metrics import conservative_rank, summarize_ranks
from retrieval_data_quality import normalized
from run001_lexical_validation import CharacterBM25
from run001_retrieval import STAGE_FORMAT, _encode_one, encode_positive_pairs
from run001_smoke import RUN_ID
from spec import Config, parameter_counts


def select_validation_pairs(corpus, limit=64):
    if type(limit) is not int or limit < 2:
        raise ValueError('need at least two English validation pairs')
    train_text, train_groups, train_media = set(), set(), set()
    for kind in KINDS:
        for row in corpus.rows(kind, 'train'):
            values = [row.get('text'), row.get('positive'), row.get('ocr'),
                      row.get('detailed_caption'), row.get('subtitles')]
            if isinstance(row.get('negatives'), list):
                values.extend(row['negatives'])
            train_text.update(value for text in values if (value := normalized(text)))
            if kind == 'video_text':
                dataset, group, media = row.get('dataset_id'), row.get('group_id'), row.get('media_sha256')
                if isinstance(dataset, str) and dataset and isinstance(group, str) and group:
                    train_groups.add((dataset, group))
                if isinstance(media, str) and media:
                    train_media.add(media)
    candidates = []
    for row in corpus.rows('video_text', 'validation'):
        if row.get('language') != 'en':
            continue
        q, p = normalized(row.get('text')), normalized(row.get('detailed_caption'))
        dataset, group, media = row.get('dataset_id'), row.get('group_id'), row.get('media_sha256')
        if (len(q) < 20 or len(p) < 20 or q == p or not isinstance(dataset, str) or not dataset or
                not isinstance(group, str) or not group or not isinstance(media, str) or len(media) != 64):
            continue
        if q in train_text or p in train_text or (dataset, group) in train_groups or media in train_media:
            continue
        pair = (row['text'].strip(), row['detailed_caption'].strip())
        raw = json.dumps([dataset, group, media, *pair], ensure_ascii=False,
                         separators=(',', ':')).encode('utf-8')
        candidates.append((hashlib.sha256(raw).digest(), (dataset, group), q, p, pair))
    selected, used_groups, used_queries, used_documents = [], set(), set(), set()
    for _, group, q, p, pair in sorted(candidates):
        if group in used_groups or q in used_queries or p in used_documents:
            continue
        selected.append(pair)
        used_groups.add(group); used_queries.add(q); used_documents.add(p)
        if len(selected) == limit:
            break
    if len(selected) < limit:
        raise ValueError(f'only {len(selected)} isolated unique video validation pairs available')
    digest = hashlib.sha256(b'posta-video-caption-validation-v1')
    for pair in selected:
        raw = json.dumps(pair, ensure_ascii=False, separators=(',', ':')).encode('utf-8')
        digest.update(len(raw).to_bytes(8, 'big')); digest.update(raw)
    return selected, digest.hexdigest()


def evaluate(dataset, tokenizer_path, checkpoint_path, limit=64):
    if not torch.cuda.is_available():
        raise RuntimeError('video-caption text comparison requires CUDA')
    corpus = Corpus(dataset)
    pairs, pairs_sha = select_validation_pairs(corpus, limit)
    tokenizer_path = Path(tokenizer_path)
    tokenizer_sha = hashlib.sha256(tokenizer_path.read_bytes()).hexdigest()
    state = torch.load(checkpoint_path, map_location='cpu', weights_only=True)
    settings = state.get('settings', {})
    if (state.get('format') != STAGE_FORMAT + '-best' or settings.get('run_id') != RUN_ID or
            settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('tokenizer_sha256') != tokenizer_sha or
            type(state.get('step')) is not int or state['step'] < 1):
        raise ValueError('checkpoint, corpus or tokenizer mismatch')
    config = Config(**settings['config'])
    if config.text_pooling != 'mean-residual' or parameter_counts(config)['total'] != 235179777:
        raise ValueError('checkpoint is not the selected retrieval architecture')
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    for token, expected in (('[PAD]', 0), ('[CLS]', 1), ('[SEP]', 2)):
        if tokenizer.token_to_id(token) != expected:
            raise ValueError('tokenizer special IDs mismatch')
    encoded = encode_positive_pairs(tokenizer, pairs, settings['max_length'])
    if len({doc for _, doc in encoded}) != len(encoded):
        raise ValueError('tokenization collapsed distinct caption candidates')
    docs = [p for _, p in pairs]
    word_indexed = [words(p) for p in docs]
    char_index = CharacterBM25(docs)
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    model = PostaMulti(config, dropout=0).half().cuda()
    model.load_state_dict(state['model'])
    model.eval()
    ranks = {'model': [], 'word_bm25': [], 'fixed_lexical_fusion': []}
    with torch.no_grad():
        vectors = torch.stack([_encode_one(model, p, 'cuda').float().cpu() for _, p in encoded])
        for index, ((query, _), (query_ids, _)) in enumerate(zip(pairs, encoded)):
            model_scores = (vectors @ _encode_one(model, query_ids, 'cuda').float().cpu()).tolist()
            word_scores = bm25_scores(query, word_indexed)
            char_scores = char_index.scores(query)
            fused = fuse_scores(word_scores, char_scores, semantic_weight=.5)
            for name, scores in (('model', model_scores), ('word_bm25', word_scores),
                                 ('fixed_lexical_fusion', fused)):
                ranks[name].append(conservative_rank(scores, index))
    return {'format': 'posta-run001-video-caption-validation-v1',
            'manifest_sha256': corpus.manifest_hash, 'tokenizer_sha256': tokenizer_sha,
            'checkpoint_sha256': file_digest(checkpoint_path), 'checkpoint_step': state['step'],
            'validation_pairs_sha256': pairs_sha, 'samples': len(pairs),
            'candidate_pool_size': len(docs),
            'weak_video_caption_supervision': bool(settings.get('video_caption_cap', 0)),
            'metrics': {name: summarize_ranks(values) for name, values in ranks.items()},
            'limitations': ['same-video captions, not independently labeled search queries',
                            'text embeddings only; video encoder not evaluated',
                            'unjudged captions may also be relevant',
                            'only exact train/validation overlaps screened',
                            'small local validation set, not independent test data'],
            'production_promotion_allowed': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError('video-caption report already exists')
    report = evaluate(args.dataset, args.tokenizer, args.checkpoint)
    atomic_report(args.output, report)
    print(json.dumps(report, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
