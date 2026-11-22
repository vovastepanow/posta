"""Count exact normalized train/frozen-benchmark overlaps without exporting text.

This is a conservative exact-match screen, not evidence of independence from
paraphrases, translated documents, substrings, or overlapping web sources.
One existing train or validation split is inspected at a time; the snapshot is
never used to select training records or tune a model.
"""
import argparse
from collections import Counter
import hashlib
import json
import unicodedata
from pathlib import Path

from corpus import Corpus, KINDS
from frozen_nanomiracl_eval import load_frozen
from compare_checkpoints import atomic_report


def fingerprint(value):
    if not isinstance(value, str):
        return None
    normalized = ' '.join(unicodedata.normalize('NFKC', value).casefold().split())
    return hashlib.sha256(normalized.encode('utf-8')).digest() if normalized else None


def audit(groups, corpus, *, split='train'):
    if split not in ('train', 'validation'):
        raise ValueError('only train and validation overlap checks are supported')
    benchmark = {}
    for language, group in groups.items():
        for role in ('queries', 'corpus'):
            benchmark[(language, role)] = {
                fp for value in group[role].values() if (fp := fingerprint(value))}
    matched = {key: set() for key in benchmark}
    rows = Counter()
    fields = Counter()
    for kind in KINDS:
        for row in corpus.rows(kind, split):
            values = [row.get('text'), row.get('positive'), row.get('ocr'),
                      row.get('detailed_caption'), row.get('subtitles')]
            negatives = row.get('negatives')
            if isinstance(negatives, list):
                values.extend(negatives)
            row_matches = set()
            for text in values:
                if not isinstance(text, str):
                    continue
                # Stage 1 clips to 3072 characters; include full text too.
                fingerprints = {fingerprint(text), fingerprint(text[:3072])} - {None}
                for key, index in benchmark.items():
                    intersection = fingerprints & index
                    if intersection:
                        matched[key].update(intersection)
                        row_matches.add(key)
                        fields[(kind, *key)] += 1
            for key in row_matches:
                rows[(kind, *key)] += 1
    return {
        'unique_benchmark_texts_matched': {
            f'{language}:{role}': len(values)
            for (language, role), values in sorted(matched.items())},
        f'matched_{split}_rows_by_kind_and_benchmark': {
            ':'.join(key): count for key, count in sorted(rows.items())},
        f'matched_{split}_fields_by_kind_and_benchmark': {
            ':'.join(key): count for key, count in sorted(fields.items())},
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', type=Path, required=True)
    parser.add_argument('--frozen', type=Path, required=True)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--split', choices=('train', 'validation'), default='train')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError('refuse to overwrite overlap report')
    groups, _, frozen_sha = load_frozen(args.frozen, args.baseline)
    corpus = Corpus(args.dataset)
    result = {'format': f'posta-nanomiracl-exact-{args.split}-overlap-v1',
              'manifest_sha256': corpus.manifest_hash, 'frozen_file_sha256': frozen_sha,
              'scope': f'entire screened {args.split} split, not just selected examples',
              'limitations': ['exact NFKC/casefold/whitespace matches only',
                              'near-duplicates, substrings and translations are not detected'],
              **audit(groups, corpus, split=args.split)}
    atomic_report(args.output, result)
    print(json.dumps(result, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
