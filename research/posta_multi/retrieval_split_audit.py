"""Read-only audit of retrieval train/validation/test isolation and annotation coverage.

This script reports aggregate counts only: no raw posts, queries, media,
record IDs or per-text hashes are emitted. Exact normalized overlap does NOT
prove corpus contamination, nor does absence establish near-duplicate safety.
"""
import argparse
from collections import Counter, defaultdict
import hashlib
import json
from pathlib import Path
import unicodedata

from corpus import Corpus
from compare_checkpoints import atomic_report, DATASET, ROOT

SPLITS = ('train', 'validation', 'test')
ROLES = ('query', 'positive', 'negative')


def normalize_text(value):
    if not isinstance(value, str):
        return ''
    return ' '.join(unicodedata.normalize('NFKC', value).casefold().split())


def fingerprint(value):
    value = normalize_text(value)
    return hashlib.sha256(value.encode('utf-8')).digest() if value else None


def audit_retrieval_splits(corpus):
    counts = {s: {'rows': 0, 'with_explicit_negatives': 0,
                  'without_explicit_negatives': 0, 'languages': {},
                  'invalid_or_missing_positives': 0, 'negative_equals_positive': 0,
                  'negative_equals_query': 0, 'duplicate_negative_values': 0}
              for s in SPLITS}
    pairs_by_split = {s: set() for s in SPLITS}
    roles_by_split = {s: {r: set() for r in ROLES} for s in SPLITS}
    source_counts = {s: Counter() for s in SPLITS}

    for split in SPLITS:
        data = counts[split]
        langs = Counter()
        for row in corpus.rows('retrieval', split):
            data['rows'] += 1
            source_counts[split][row.get('dataset_id', 'unknown')] += 1
            langs[row.get('language') or 'und'] += 1
            q = fingerprint(row.get('text'))
            p = fingerprint(row.get('positive'))
            if not q or not p:
                data['invalid_or_missing_positives'] += 1
            if q:
                roles_by_split[split]['query'].add(q)
            if p:
                roles_by_split[split]['positive'].add(p)
            if q and p:
                pairs_by_split[split].add(hashlib.sha256(q + p).digest())
            negatives = row.get('negatives') or []
            if not isinstance(negatives, list):
                negatives = []
            clean_negatives = [fp for item in negatives if (fp := fingerprint(item))]
            if clean_negatives:
                data['with_explicit_negatives'] += 1
            else:
                data['without_explicit_negatives'] += 1
            data['duplicate_negative_values'] += len(clean_negatives) - len(set(clean_negatives))
            for n in clean_negatives:
                roles_by_split[split]['negative'].add(n)
                data['negative_equals_positive'] += int(n == p)
                data['negative_equals_query'] += int(n == q)
        data['languages'] = dict(sorted(langs.items()))

    overlaps = {}
    for a, b in (('train','validation'), ('train','test'), ('validation','test')):
        cross = {
            'exact_query_positive_pairs': len(pairs_by_split[a] & pairs_by_split[b]),
        }
        for role in ROLES:
            cross[f'exact_shared_{role}_texts'] = len(roles_by_split[a][role] & roles_by_split[b][role])
        cross['positive_in_other_negatives'] = len(
            roles_by_split[a]['positive'] & roles_by_split[b]['negative'])
        cross['negative_in_other_positives'] = len(
            roles_by_split[a]['negative'] & roles_by_split[b]['positive'])
        overlaps[f'{a}_vs_{b}'] = cross

    return {
        'format': 'posta-retrieval-split-audit-v1',
        'manifest_sha256': getattr(corpus, 'manifest_hash', None),
        'splits': counts,
        'dataset_counts': {s: dict(sorted(source_counts[s].items())) for s in SPLITS},
        'exact_cross_split_overlap': overlaps,
        'limitations': [
            'Exact NFKC, casefold and collapsed-whitespace matching only',
            'No near-duplicate, translation, source-family or benchmark isolation proof',
            'The same document might legitimately appear for distinct query contexts',
            'Rights, privacy and relevance annotations remain unreviewed',
            'All outputs are aggregate counters, never input text or document identifiers',
        ],
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', default=str(DATASET))
    p.add_argument('--output', default=str(ROOT / 'posta-multi-retrieval-split-audit-20261121-v1.json'))
    args = p.parse_args()
    report = audit_retrieval_splits(Corpus(args.dataset))
    atomic_report(Path(args.output), report)
    print(json.dumps({
        'format': report['format'],
        'row_counts': {s: report['splits'][s]['rows'] for s in SPLITS},
        'overlaps': report['exact_cross_split_overlap'],
        'report_file': args.output,
    }, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
