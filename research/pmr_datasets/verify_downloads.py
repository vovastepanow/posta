#!/usr/bin/env python3
"""Independently hash all fully downloaded source files; no network or extraction."""
import argparse
import hashlib
import json
from pathlib import Path
from datetime import datetime, timezone
from probe import atomic_json


def digest(path):
    value = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def verify(root):
    results, pending = [], []
    for manifest in sorted(root.glob('manifest-*.json')):
        for id_, row in json.loads(manifest.read_text())['records'].items():
            if row.get('status') not in ('verified', 'verified_existing'):
                pending.append({'id':id_, 'status':row.get('status')})
                continue
            entry = {'id':id_, 'path':row['local_path'], 'revision':row['revision'],
                     'expected_sha256':row['expected_sha256'], 'expected_bytes':row['expected_bytes']}
            try:
                path = Path(row['local_path'])
                entry['actual_bytes'] = path.stat().st_size
                entry['actual_sha256'] = digest(path)
                entry['status'] = ('ok' if entry['actual_bytes'] == entry['expected_bytes'] and
                                   entry['actual_sha256'] == entry['expected_sha256'] else 'mismatch')
            except OSError as e:
                entry.update(status='error', error=str(e)[:250])
            results.append(entry)
            print(id_, entry['status'], flush=True)
    return {'checked_utc':datetime.now(timezone.utc).isoformat(), 'verified_files':results,
            'pending_at_start':pending, 'note':'Checks only completed files present in manifests; no benchmark or licensing clearance.'}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory', type=Path)
    p.add_argument('--out', type=Path, required=True)
    args = p.parse_args()
    result = verify(args.directory)
    atomic_json(args.out, result)
    return int(any(x['status'] != 'ok' for x in result['verified_files']))


if __name__ == '__main__':
    raise SystemExit(main())
