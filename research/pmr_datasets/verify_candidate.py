#!/usr/bin/env python3
"""Check local candidate hashes, provenance and exact split isolation; no network."""
import argparse
import hashlib
import json
import subprocess
from collections import Counter
from pathlib import Path
from prepare import auxiliary_texts, clean, normalize


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def verify(root, decode_media=False):
    manifest = json.loads((root / 'manifest.json').read_text(encoding='utf-8'))
    counts, identities, fingerprints, media = Counter(), {}, set(), set()
    for field in ('duplicates_report', 'rejections_report', 'benchmark_exclusions'):
        entry = manifest.get(field)
        if entry and entry.get('path'):
            report = (root / entry['path']).resolve()
            if report.parent != root.resolve() or digest(report) != entry['sha256']:
                raise ValueError(field + ' path or hash mismatch')
    for item in manifest['files']:
        path = (root / item['path']).resolve()
        if path.parent != root.resolve() or digest(path) != item['sha256']:
            raise ValueError('record file path or hash mismatch')
        seen_count = 0
        with path.open(encoding='utf-8') as f:
            for line in f:
                row = json.loads(line)
                seen_count += 1
                kind, split = row['kind'], row['split']
                if path.name != f'{kind}-{split}.jsonl' or row.get('training_approved') is not False:
                    raise ValueError('task, split or quarantine marker mismatch')
                source = manifest['sources'].get(row['dataset_id']) or {}
                if (row['dataset_revision'] != source.get('revision') or
                        row['source_file_sha256'] != source.get('expected_sha256') or
                        row['source_file'] != source.get('source_path')):
                    raise ValueError('record source provenance mismatch')
                fingerprint = normalize(row)['_fingerprint']
                if fingerprint in fingerprints:
                    raise ValueError('exact duplicate remains')
                fingerprints.add(fingerprint)
                keys = [('text', clean(t).casefold()) for t in
                        [row['text'], row.get('positive', ''), *auxiliary_texts(row),
                         *row.get('negatives', [])] if clean(t)]
                keys.append(('group', row['dataset_id'] + ':' + row['group_id']))
                if clean(row.get('source_url')):
                    keys.append(('source_url', clean(row['source_url'])))
                if row['dataset_id'] in ('fineweb2-ru', 'fineweb2-hq-ru'):
                    keys.append(('source_family_id', 'fineweb2:' + row['record_id']))
                if row.get('media_sha256'):
                    sha = row['media_sha256']
                    keys.append(('media', sha))
                    path_media = (root / row['media']['path']).resolve()
                    if path_media.parent != (root / 'media').resolve() or row['media']['sha256'] != sha:
                        raise ValueError('invalid media path or digest')
                    if sha not in media:
                        if digest(path_media) != sha:
                            raise ValueError('media hash mismatch')
                        if decode_media:
                            if kind in ('speech_text', 'video_text'):
                                command = ['ffmpeg', '-v', 'error', '-i', str(path_media)]
                                if kind == 'video_text':
                                    command += ['-map', '0:v:0']
                                result = subprocess.run(command + ['-f', 'null', '-'],
                                                        capture_output=True, timeout=60)
                                if result.returncode:
                                    raise ValueError('audio or video decoding failed')
                            else:
                                from PIL import Image
                                with Image.open(path_media) as image:
                                    image.load()
                        media.add(sha)
                for key in keys:
                    if key in identities and identities[key] != split:
                        raise ValueError('exact content or source group crosses splits')
                    identities[key] = split
                counts[(kind, split, row['language'])] += 1
        if seen_count != item['rows']:
            raise ValueError('row count mismatch')
    expected = {(r['kind'], r['split'], r['language']): r['rows'] for r in manifest['counts']}
    if dict(counts) != expected or sum(counts.values()) != manifest['accepted']:
        raise ValueError('manifest counts mismatch')
    media_files_on_disk = sum(1 for p in (root / 'media').iterdir() if p.is_file())
    return {'status': 'exact integrity verified; source and benchmark review still required',
            'records': sum(counts.values()), 'media_files': len(media),
            'unreferenced_media_files': media_files_on_disk - len(media),
            'splits': {s: sum(n for (kind, split, lang), n in counts.items() if split == s)
                       for s in ('train', 'validation', 'test')}}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory', type=Path)
    p.add_argument('--decode-media', action='store_true')
    args = p.parse_args()
    print(json.dumps(verify(args.directory, args.decode_media), ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
