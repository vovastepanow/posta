#!/usr/bin/env python3
"""Show actual verified and partial bytes for parallel shard downloads."""
import argparse
import json
from pathlib import Path
from download_shards import PLAN


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('directory', type=Path)
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
    print(json.dumps({'sources': files, 'total_bytes_on_disk': sum(x.get('bytes_on_disk', 0) for x in files),
                      'blocked': {'megapairs': 'gated access', 'common-voice': 'empty HF mirror'}},
                     ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
