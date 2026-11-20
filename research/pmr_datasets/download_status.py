#!/usr/bin/env python3
"""Show actual verified and partial bytes for parallel shard downloads."""
import argparse
import json
from pathlib import Path
from download_shards import PLAN


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory', type=Path)
    p.add_argument('--compact', action='store_true', help='show a concise human-readable progress table')
    args = p.parse_args()
    files = []
    for path in sorted(args.directory.glob('manifest-*.json')):
        manifest = json.loads(path.read_text())
        for item in manifest['records'].values():
            record = dict(item)
            local = Path(record.get('local_path', ''))
            if record.get('local_path'):
                part = local.with_name(local.name + '.part')
                record['bytes_on_disk'] = local.stat().st_size if local.is_file() else part.stat().st_size if part.is_file() else 0
            files.append(record)
    known = {x['dataset_id'] for x in files}
    for id_, repo, revision, source_path in PLAN:
        if id_ not in known:
            files.append({'dataset_id': id_, 'repository': repo, 'revision': revision,
                          'source_path': source_path, 'status': 'queued', 'bytes_on_disk': 0})
    total = sum(x.get('bytes_on_disk', 0) for x in files)
    if args.compact:
        print(f'На диске: {total / (1024 ** 3):.2f} ГиБ')
        print(f'Готово: {sum(x["status"] in ("verified", "verified_existing") for x in files)}')
        print()
        for item in files:
            if item['status'] not in ('downloading', 'error'):
                continue
            size = item.get('expected_bytes', 0)
            done = item.get('bytes_on_disk', 0)
            percentage = f'{100 * done / size:.1f}%' if size else '?'
            detail = item.get('error', '')
            print(f'{item["dataset_id"]}: {item["status"]} {percentage} {detail}'.rstrip())
    else:
        print(json.dumps({'sources': files, 'total_bytes_on_disk': total,
                          'blocked': {'megapairs': 'gated access', 'common-voice': 'empty HF mirror'}},
                         ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
