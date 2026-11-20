#!/usr/bin/env python3
"""Collect bounded, unapproved HF previews for inspection, never for training.

Dataset Server previews are not revision-pinned; manifest records this limitation.
Never accepts upstream terms or fetches source archives. Existing output is not overwritten.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import urllib.parse
import urllib.request
from datetime import datetime, timezone

from probe import CATALOG, atomic_json, dataset_api_url, get_json, server_url

MAX_IMAGE = 4 * 1024 * 1024
SUPPORTED = {
    'fineweb2-ru': ('rus_Cyrl', 'train', 'text', 'ru'),
    'fineweb-en': ('default', 'train', 'text', 'en'),
    'kalm-retrieval': ('default', 'train', 'retrieval', 'und'),
    'wit': ('default', 'train', 'image_text', 'und'),
}


def image_bytes(url):
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != 'https' or parsed.hostname != 'datasets-server.huggingface.co':
        raise ValueError('media URL is not a dataset-server HTTPS asset')
    req = urllib.request.Request(url, headers={'User-Agent': 'Posta-PMR-dataset-audit/0.1'})
    with urllib.request.urlopen(req, timeout=20) as response:
        if urllib.parse.urlparse(response.geturl()).hostname != 'datasets-server.huggingface.co':
            raise ValueError('media redirected outside dataset server')
        data = response.read(MAX_IMAGE + 1)
    if len(data) > MAX_IMAGE:
        raise ValueError('media exceeds 4 MiB limit')
    if not (data.startswith(b'\xff\xd8\xff') or data.startswith(b'\x89PNG\r\n\x1a\n')):
        raise ValueError('media is not JPEG or PNG')
    return data


def collect(item, count, directory, media):
    config, split, kind, language = SUPPORTED[item['id']]
    revision = get_json(dataset_api_url(item['repo'])).get('sha')
    if not revision:
        raise ValueError('source has no revision')
    response = get_json(server_url('first-rows', dataset=item['repo'], config=config, split=split))
    raw_rows = response.get('rows') or []
    selected = raw_rows[:count]
    if not selected:
        raise ValueError('empty preview')
    records = []
    for entry in selected:
        row = entry['row']
        idx = entry.get('row_idx')
        record = {'dataset_id': item['id'], 'dataset_revision_observed': revision,
                  'row_index': idx, 'kind': kind, 'language': language, 'source_split': split,
                  'training_approved': False, 'annotation_origin': 'upstream preview'}
        if kind == 'text':
            record.update(record_id=str(row.get('id') or idx), text=row.get('text'),
                          source_url=row.get('url'), group_id=str(row.get('id') or idx))
        elif kind == 'retrieval':
            record.update(record_id=str(idx), text=row.get('query'), positives=row.get('pos'),
                          negatives=row.get('neg'), group_id=str(idx))
        else:
            info = row.get('image') or {}
            record.update(record_id=str(idx), text=row.get('caption_attribution_description'),
                          source_url=row.get('image_url'), metadata_url=row.get('metadata_url'),
                          media={'width': info.get('width'), 'height': info.get('height'),
                                 'bytes_verified': False}, group_id=str(row.get('image_url') or idx))
            if media and info.get('src'):
                try:
                    data = image_bytes(info['src'])
                    digest = hashlib.sha256(data).hexdigest()
                    suffix = '.jpg' if data.startswith(b'\xff\xd8\xff') else '.png'
                    name = digest + suffix
                    (directory / 'media').mkdir(exist_ok=True)
                    (directory / 'media' / name).write_bytes(data)
                    record['media'].update(sha256=digest, path='media/' + name,
                                           bytes=len(data), bytes_verified=True)
                except (OSError, ValueError) as exc:
                    record['media']['error'] = str(exc)[:200]
        records.append(record)
    if get_json(dataset_api_url(item['repo'])).get('sha') != revision:
        raise ValueError('source HEAD changed; discard preview')
    data = ''.join(json.dumps(row, ensure_ascii=False, sort_keys=True) + '\n' for row in records).encode()
    filename = item['id'] + '.jsonl'
    (directory / filename).write_bytes(data)
    return {'id': item['id'], 'repo': item['repo'], 'observed_head': revision,
            'config': config, 'split': split, 'records': len(records), 'file': filename,
            'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data),
            'media_verified': sum(bool(r.get('media', {}).get('bytes_verified')) for r in records),
            'license_claim': item['license_claim'], 'license_review': 'pending',
            'revision_binding': 'HEAD checked before and after; Dataset Server cache is not revision-pinned'}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--out-dir', type=Path, required=True)
    p.add_argument('--ids', nargs='+', choices=sorted(SUPPORTED), default=list(SUPPORTED))
    p.add_argument('--count', type=int, default=10)
    p.add_argument('--media', action='store_true', help='fetch at most count bounded preview images')
    args = p.parse_args()
    if not 1 <= args.count <= 20:
        p.error('--count must be between 1 and 20')
    args.out_dir.mkdir(parents=True, exist_ok=False)
    catalog = json.loads(CATALOG.read_text())
    manifest = {'status': 'QUARANTINED: inspection only, not approved for training',
                'created_utc': datetime.now(timezone.utc).isoformat(), 'sources': [],
                'limitations': ['Preview is not representative', 'source licenses and media rights pending',
                                'benchmark overlap, PII and near duplicates not screened',
                                'observed HEAD does not prove Dataset Server cache revision']}
    for id_ in args.ids:
        item = next(x for x in catalog['datasets'] if x['id'] == id_)
        try:
            manifest['sources'].append(collect(item, args.count, args.out_dir, args.media))
        except (OSError, ValueError, KeyError) as exc:
            manifest['sources'].append({'id': id_, 'error': str(exc)[:300]})
    atomic_json(args.out_dir / 'manifest.json', manifest)
    print(json.dumps(manifest, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
