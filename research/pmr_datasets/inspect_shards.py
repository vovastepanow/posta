#!/usr/bin/env python3
"""Inspect only completed source shards; never extract archives or print row contents.

Parquet inspection requires pyarrow; HDF5 requires h5py. Each source is isolated
on error. Passing a download manifest never causes downloads or DB connections.
"""
import argparse
import json
import tarfile
from pathlib import Path
from datetime import datetime, timezone
from probe import atomic_json


def inspect(path):
    if path.suffix == '.parquet':
        import pyarrow.parquet as pq
        f = pq.ParquetFile(path)
        return {'format': 'parquet', 'row_count': f.metadata.num_rows,
                'row_groups': f.metadata.num_row_groups,
                'columns': [{'name': x.name, 'type': str(x.type)} for x in f.schema_arrow]}
    if path.suffix in ('.h5', '.hdf5'):
        import h5py
        with h5py.File(path, 'r') as f:
            roots = []
            for key in list(f.keys())[:30]:
                value = f[key]
                roots.append({'name': key, 'type': type(value).__name__,
                              'shape': list(value.shape) if isinstance(value, h5py.Dataset) else None,
                              'dtype': str(value.dtype) if isinstance(value, h5py.Dataset) else None})
        return {'format': 'hdf5', 'root_entries': roots, 'limited_to': 30}
    if path.name.endswith(('.tar', '.tar.gz')):
        names = []
        with tarfile.open(path, mode='r|*') as archive:
            for entry in archive:
                names.append({'name': entry.name, 'size_bytes': entry.size, 'is_file': entry.isfile()})
                if len(names) == 20:
                    break
        return {'format': 'tar', 'first_entries': names, 'limited_to': 20}
    if path.suffix == '.tsv':
        with path.open('rb') as stream:
            line = stream.readline(1024 * 1024)
        return {'format': 'tsv', 'first_line_bytes': len(line),
                'first_line_fields': len(line.rstrip(b'\r\n').split(b'\t'))}
    return {'format': 'unknown'}


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--manifest', type=Path, action='append', required=True)
    p.add_argument('--out', type=Path, required=True)
    args = p.parse_args()
    results = []
    for manifest_path in args.manifest:
        data = json.loads(manifest_path.read_text())
        for id_, row in data['records'].items():
            if row['status'] not in ('verified', 'verified_existing'):
                continue
            result = {'id': id_, 'revision': row['revision'], 'path': row['local_path'],
                      'sha256_previously_verified': row['expected_sha256']}
            try:
                result.update(inspect(Path(row['local_path'])))
                result['status'] = 'ok'
            except (Exception, ImportError) as exc:
                result.update(status='error', error=str(exc)[:300])
            results.append(result)
    atomic_json(args.out, {'checked_utc': datetime.now(timezone.utc).isoformat(), 'sources': results,
                          'note': 'Header/schema check only; no rights clearance or quality certification.'})
    print(json.dumps(results, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
