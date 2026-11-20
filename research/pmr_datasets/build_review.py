#!/usr/bin/env python3
"""Build a non-training review pilot from sample_preview.py output; no network."""
import argparse
import hashlib
import json
from collections import Counter
from pathlib import Path

from prepare import normalize, partition, write_jsonl
from probe import atomic_json


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def build(source_dir, output):
    manifest = json.loads((source_dir / 'manifest.json').read_text(encoding='utf-8'))
    rows, provenance, rejected = [], [], []
    sources = [s for s in manifest['sources'] if 'file' in s]
    for source in sources:
        path = source_dir / source['file']
        if path.resolve().parent != source_dir.resolve() or digest(path) != source['sha256']:
            raise ValueError('input file path or SHA256 mismatch')
        for line in path.read_text(encoding='utf-8').splitlines():
            raw = json.loads(line)
            try:
                if (raw.get('dataset_id') != source['id'] or
                        raw.get('dataset_revision_observed') != source['observed_head'] or
                        raw.get('source_split') != source['split']):
                    raise ValueError('row source provenance disagrees with manifest')
                if raw.get('training_approved') is not False:
                    raise ValueError('expected explicitly unapproved inspection record')
                converted = {'dataset_id': raw['dataset_id'], 'record_id': raw['record_id'],
                             'kind': raw['kind'], 'language': raw['language'], 'text': raw['text'],
                             'group_id': raw['group_id']}
                if raw['kind'] == 'retrieval':
                    positives = raw.get('positives') or []
                    if len(positives) != 1:
                        raise ValueError('pilot requires exactly one positive')
                    converted.update(positive=positives[0], negatives=raw.get('negatives') or [])
                if raw['kind'] == 'image_text':
                    media = raw.get('media') or {}
                    relative = Path(media.get('path', ''))
                    file = (source_dir / relative).resolve()
                    if not media.get('bytes_verified') or file.parent != (source_dir / 'media').resolve():
                        raise ValueError('image bytes unavailable or invalid path')
                    if digest(file) != media.get('sha256'):
                        raise ValueError('image SHA256 mismatch')
                    converted['media_sha256'] = media['sha256']
                row = normalize(converted)
                rows.append(row)
                provenance.append({'source_revision_observed': raw['dataset_revision_observed'],
                                   'source_split': raw['source_split'], 'source_row_index': raw['row_index'],
                                   'annotation_origin': raw['annotation_origin'],
                                   'source_url': raw.get('source_url'), 'metadata_url': raw.get('metadata_url'),
                                   'media': raw.get('media'), 'training_approved': False})
            except (ValueError, KeyError, TypeError) as exc:
                rejected.append({'dataset_id': raw.get('dataset_id'), 'record_id': raw.get('record_id'),
                                 'reason': str(exc)[:200]})
    assignments = partition(rows)
    grouped, seen, duplicates = {}, set(), []
    for i, row in enumerate(rows):
        if row['_fingerprint'] in seen:
            duplicates.append({'dataset_id': row['dataset_id'], 'record_id': row['record_id']})
            continue
        seen.add(row['_fingerprint'])
        result = {k: v for k, v in row.items() if not k.startswith('_')}
        result.update(provenance[i])
        result['split'] = assignments[i]
        grouped.setdefault((row['kind'], assignments[i]), []).append(result)
    output.mkdir(parents=True, exist_ok=False)
    files, counts = [], Counter()
    for (kind, split), records in sorted(grouped.items()):
        name = kind + '-' + split + '.jsonl'
        path = output / name
        write_jsonl(path, records)
        files.append({'file': name, 'sha256': digest(path), 'rows': len(records)})
        for row in records:
            counts[(kind, split, row['language'])] += 1
    result = {'status': 'QUARANTINED: review only; not training ready', 'version': 'posta-dataset-v0.1-review',
              'source_manifest_sha256': digest(source_dir / 'manifest.json'),
              'source_directory': str(source_dir.resolve()), 'source_manifests': sources,
              'files': files, 'counts': [{'kind': k, 'split': s, 'language': l, 'rows': n}
                                          for (k, s, l), n in sorted(counts.items())],
              'input_rows': len(rows) + len(rejected), 'accepted_rows': sum(c for c in counts.values()),
              'exact_duplicates_removed': len(duplicates), 'duplicates': duplicates, 'rejected': rejected,
              'limitations': ['No rights clearance or training approval', 'Preview cache revision not pinned',
                              'No independent benchmark contamination or near-duplicate check',
                              'Language values are source labels, not independently verified',
                              'Image paths are relative to source_directory; media rights not verified',
                              'Local validation/test are not independent benchmark sets']}
    atomic_json(output / 'manifest.json', result)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source-dir', type=Path, required=True)
    parser.add_argument('--out-dir', type=Path, required=True)
    args = parser.parse_args()
    print(json.dumps(build(args.source_dir, args.out_dir), ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
