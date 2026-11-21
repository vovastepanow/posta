#!/usr/bin/env python3
"""Inventory EXISTING HDD source bytes and unused Parquet rows (read-only).

No network, no dataset decoding, no new files except one aggregate JSON report.
The report contains ONLY public source IDs, sizes and row counts. No raw text,
private paths, URLs, contributor records, personal content or checksums copied.
Verified means previously SHA-checked bytes, not licensed training samples.
"""
import argparse
from collections import Counter
import json
import os
from pathlib import Path
import shutil
from datetime import datetime, timezone

FORMAT = 'posta-multi-235m-local-hdd-inventory-v1'
DOWNLOAD_DIR = 'downloads-v0.1'
CANDIDATE_DIR = 'Posta-Dataset-v0.1-screened'
SKIP = {'tmp', 'torch-cu126', 'python-libs', 'tokenizer-libs', '.cache'}

ROLES = {
    'fineweb2-ru': 'text_ru',
    'fineweb2-hq-ru': 'text_ru',
    'fineweb-en': 'text_en',
    'kalm-retrieval': 'retrieval_zh',
    'bge-m3': 'retrieval_unreviewed',
    'mmarco-ru': 'retrieval_rights_blocked',
    'mmarco-ru-queries': 'retrieval_rights_blocked',
    'mmarco-train-ids': 'retrieval_rights_blocked',
    'nvidia-ocr-ru': 'ocr_ru',
    'nvidia-ocr-en': 'ocr_en',
    'librispeech': 'speech_en',
    'wit': 'image_caption',
    'memexplain': 'meme_text',
    'videoufo': 'video_archive',
    'videoufo-annotations': 'video_annotations',
    'bvd-video': 'url_metadata_only',
    'datacomp-hq': 'url_metadata_only',
}
def role_of(name):
    if name.startswith(('mteb-ria-eval', 'mmeb-v3-eval')):
        return 'evaluation_only'
    if name.startswith('memexplain-'):
        return 'meme_text'
    return ROLES.get(name, 'other_research_candidate')


def candidate_summary(root):
    path = root / CANDIDATE_DIR / 'manifest.json'
    if not path.is_file():
        return {'manifest_exists': False}
    info = json.loads(path.read_text(encoding='utf-8'))
    counts = info.get('counts') or []
    total = info.get('accepted', None)
    by_kind = Counter()
    by_split = Counter()
    for item in counts:
        if not isinstance(item, dict):
            continue
        rows = item.get('rows')
        if type(rows) is int and rows >= 0:
            by_kind[item.get('kind', 'unknown')] += rows
            by_split[item.get('split', 'unknown')] += rows
    return {
        'manifest_exists': True,
        'status': info.get('status'),
        'approved_for_training': info.get('status') == 'approved_for_training'
                                 and info.get('privacy_review') == 'passed'
                                 and info.get('source_rights_review') == 'passed',
        'accepted_records': total,
        'counts_by_kind': dict(sorted(by_kind.items())),
        'counts_by_split': dict(sorted(by_split.items())),
        'manifest_file_count': len(info.get('files', [])),
        'source_count': len(info.get('sources', {})),
    }


def parquet_footer_rows(path):
    """Read just columnar Parquet footer; never rows. None if no pyarrow."""
    try:
        import pyarrow.parquet as pq
    except ImportError:
        return None
    return int(pq.ParquetFile(path).metadata.num_rows)


def inventory_downloads(root):
    directory = root / DOWNLOAD_DIR
    if not directory.is_dir():
        return {'directory_exists': False, 'files': [], 'totals': {}}
    manifest_files = sorted(directory.glob('manifest-*.json'))
    records = {}
    conflicts = []
    for manifest_path in manifest_files:
        info = json.loads(manifest_path.read_text(encoding='utf-8'))
        for key, record in info.get('records', {}).items():
            if not isinstance(record, dict):
                continue
            if key in records and records[key] != record:
                conflicts.append(key)
            records[key] = record
    files = []
    totals = Counter()
    for source, item in sorted(records.items()):
        local_name = item.get('local_path')
        if not isinstance(local_name, str) or not local_name:
            continue
        path = Path(local_name)
        try:
            resolved = path.resolve(strict=False)
            # Do not let an untrusted manifest trigger reads outside its root.
            if not resolved.is_relative_to(directory.resolve()):
                raise ValueError('source path outside existing downloads directory')
            finished = resolved.is_file()
            partial = resolved.with_name(resolved.name + '.part')
            on_disk = resolved if finished else partial
            on_disk_bytes = on_disk.stat().st_size if on_disk.is_file() else 0
            valid = finished and item.get('status') in ('verified', 'verified_existing')
            claimed = item.get('expected_bytes')
            if valid and (type(claimed) is not int or claimed != on_disk_bytes):
                valid = False
            footer_rows = None
            footer_error = None
            if valid and resolved.suffix == '.parquet':
                try:
                    footer_rows = parquet_footer_rows(resolved)
                except (OSError, ValueError, RuntimeError, ImportError) as error:
                    footer_error = type(error).__name__
            row = {
                'id': source, 'role': role_of(source),
                'state': 'verified_on_manifest' if valid else
                         ('partial' if not finished and on_disk_bytes else 'not_verified'),
                'stored_bytes': on_disk_bytes,
                'claimed_source_bytes': claimed if type(claimed) is int else None,
                'parquet_footer_rows': footer_rows,
                'parquet_footer_error': footer_error,
                'training_approval': False,
            }
        except (OSError, ValueError, RuntimeError) as error:
            row = {'id': source, 'role': role_of(source),
                   'state': 'inventory_error_' + type(error).__name__,
                   'stored_bytes': 0, 'parquet_footer_rows': None,
                   'training_approval': False}
        files.append(row)
        totals['files'] += 1
        totals['stored_bytes'] += row['stored_bytes']
        totals[row['state'] + '_files'] += 1
        if row['role'] != 'evaluation_only':
            totals['non_eval_bytes'] += row['stored_bytes']
        if row['parquet_footer_rows'] is not None:
            totals['parquet_rows_all_roles'] += row['parquet_footer_rows']
            if row['role'] in ('text_ru', 'text_en', 'retrieval_zh',
                               'meme_text', 'image_caption', 'speech_en'):
                totals['parquet_rows_candidate_sources'] += row['parquet_footer_rows']
    return {
        'directory_exists': True,
        'manifest_count': len(manifest_files),
        'conflicting_source_ids': sorted(set(conflicts)),
        'files': files,
        'totals': dict(totals),
        'limitations': [
            'Parquet footer counts are physical rows, NOT unique trainable examples',
            'Parquet rows may contain embedded media or metadata rather than post texts',
            'A checked SHA in an old manifest is not a fresh byte-for-byte audit',
            'Evaluation-only datasets and blocked-rights sets must NOT enter training',
            'Source license, consent, private information and benchmark overlaps still require review',
        ],
    }


def top_level_sizes(root, limit=300000):
    """Stat filesystem entries without reading the contents of any dataset."""
    summary = []
    for first in root.iterdir():
        if first.is_symlink() or not first.is_dir() or first.name in SKIP:
            continue
        size = 0
        count = 0
        pending = [first]
        truncated = False
        while pending:
            current = pending.pop()
            try:
                with os.scandir(current) as iterator:
                    for child in iterator:
                        if child.is_symlink():
                            continue
                        if child.is_dir(follow_symlinks=False):
                            pending.append(Path(child.path))
                        elif child.is_file(follow_symlinks=False):
                            size += child.stat(follow_symlinks=False).st_size
                            count += 1
                        if count >= limit:
                            truncated = True
                            pending.clear()
                            break
            except OSError:
                truncated = True
        summary.append({
            'directory': first.name, 'allocated_file_bytes': size,
            'files_seen': count, 'scan_truncated': truncated})
    return sorted(summary, key=lambda item: -item['allocated_file_bytes'])


def build(root, *, scan_top_level=True):
    root = Path(root).resolve(strict=True)
    if not root.is_dir():
        raise ValueError('root is not a directory')
    disk = shutil.disk_usage(root)
    return {
        'format': FORMAT, 'collected_at_utc': datetime.now(timezone.utc).isoformat(),
        'root_name': root.name,
        'disk_total_bytes': disk.total, 'disk_used_bytes': disk.used,
        'disk_free_bytes': disk.free, 'recommended_reserved_free_bytes': 40*1024**3,
        'screened_candidate': candidate_summary(root),
        'existing_downloads': inventory_downloads(root),
        'top_level_storage': top_level_sizes(root) if scan_top_level else [],
        'training_started': False, 'new_files_downloaded': 0,
        'purpose': 'Measure unused existing data before any new download or training approval',
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path,
                        default=Path('/mnt/hdd_ubuntu/posta-pmr'))
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--skip-top-level', action='store_true')
    args = parser.parse_args()
    report = build(args.root, scan_top_level=not args.skip_top_level)
    output = args.out
    if output.exists():
        raise FileExistsError('inventory already exists; refuse overwrite')
    output.parent.mkdir(parents=True, exist_ok=True)
    temp = output.with_name('.' + output.name + '.tmp')
    if temp.exists():
        raise FileExistsError('inventory temp file already exists')
    try:
        with temp.open('x', encoding='utf-8') as handle:
            json.dump(report, handle, ensure_ascii=False, indent=2, sort_keys=True)
        os.chmod(temp, 0o600)
        os.replace(temp, output)
    finally:
        if temp.exists():
            temp.unlink()
    safe = {
        'stored_gib': round(report['existing_downloads']['totals'].get(
            'stored_bytes', 0) / 1024**3, 2),
        'downloaded_source_files': report['existing_downloads']['totals'].get('files', 0),
        'screened_records': report['screened_candidate'].get('accepted_records'),
        'new_downloads': 0,
    }
    print(json.dumps(safe, ensure_ascii=False))


if __name__ == '__main__':
    main()
