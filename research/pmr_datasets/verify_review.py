#!/usr/bin/env python3
"""Verify hashes, counts and exact cross-split identities of a review pilot."""
import argparse
import hashlib
import json
from collections import Counter
from pathlib import Path

from prepare import clean


def sha_file(path):
    h = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def verify(directory):
    manifest = json.loads((directory / 'manifest.json').read_text(encoding='utf-8'))
    source = Path(manifest['source_directory'])
    if sha_file(source / 'manifest.json') != manifest['source_manifest_sha256']:
        raise ValueError('source manifest SHA256 mismatch')
    identities, counts = {}, Counter()
    for item in manifest['files']:
        path = directory / item['file']
        if path.resolve().parent != directory.resolve() or sha_file(path) != item['sha256']:
            raise ValueError('output path or SHA256 mismatch')
        rows = [json.loads(line) for line in path.read_text(encoding='utf-8').splitlines()]
        if len(rows) != item['rows']:
            raise ValueError('file row count mismatch')
        for row in rows:
            if row.get('training_approved') is not False:
                raise ValueError('record is not explicitly quarantined')
            kind, split = row['kind'], row['split']
            if path.name != f'{kind}-{split}.jsonl':
                raise ValueError('task/split mismatch')
            counts[(kind, split, row['language'])] += 1
            keys = [('text', clean(t).casefold()) for t in
                    [row['text'], row.get('positive', ''), *row.get('negatives', [])] if clean(t)]
            keys.append(('group', row['dataset_id'] + ':' + row['group_id']))
            if row.get('media_sha256'):
                keys.append(('media', row['media_sha256']))
                media = (source / row['media']['path']).resolve()
                if media.parent != (source / 'media').resolve() or sha_file(media) != row['media_sha256']:
                    raise ValueError('media path or SHA256 mismatch')
            for key in keys:
                if key in identities and identities[key] != split:
                    raise ValueError('exact content, media or group crosses splits')
                identities[key] = split
    expected = {(x['kind'], x['split'], x['language']): x['rows'] for x in manifest['counts']}
    if dict(counts) != expected or sum(counts.values()) != manifest['accepted_rows']:
        raise ValueError('manifest counts mismatch')
    return {'status': 'verified review files; training approval and benchmark isolation not assessed',
            'rows': sum(counts.values()), 'files': len(manifest['files']),
            'split_counts': dict(Counter({s: sum(n for (k, split, l), n in counts.items() if split == s)
                                           for s in ('train', 'validation', 'test')}))}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory', type=Path)
    args = p.parse_args()
    print(json.dumps(verify(args.directory), ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
