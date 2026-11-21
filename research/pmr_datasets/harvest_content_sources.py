#!/usr/bin/env python3
"""Bounded news/forum ingestion into QUARANTINE, never a trainable release.

Use only dataset-server train preview rows, not unrelated evaluation splits.
HF Dataset Server does NOT revision-pin its cached rows; verify HEAD before
and after, record the limitation explicitly, and never auto-approve.
Raw texts stay on the private Posta HDD, not in CI/logs/repository.
"""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import shutil
import unicodedata
from urllib.parse import urlparse

from probe import dataset_api_url, get_json, server_url

FORMAT = 'posta-235m-content-source-quarantine-v1'
POLICY = 'hash-offset-train-preview-news-and-discussions-v1'
SOURCES = {
    'common-pile-news-filtered': {
        'repo': 'common-pile/news_filtered',
        'revision': '59aaa8f104e189e6fb8033f0ed319c5c343a03b1',
        'offset_ceiling': 100000,
        'kind': 'news',
    },
    'common-pile-stackexchange': {
        'repo': 'common-pile/stackexchange',
        'revision': '5ec3aa2e65b7c61b7c18f1ff18ccbc4d3279a23c',
        'offset_ceiling': 1000000,
        'kind': 'community_discussion',
    },
}


def canonical_text_key(content):
    text = ' '.join(unicodedata.normalize('NFKC', content).casefold().split())
    return hashlib.sha256(text.encode('utf-8')).hexdigest() if text else None


def sampling_offset(dataset_id, batch, ceiling, batch_size):
    if batch < 0 or ceiling < batch_size or batch_size < 1:
        raise ValueError('invalid sampling window')
    digest = hashlib.sha256((POLICY + ':' + dataset_id + ':' + str(batch)).encode()).digest()
    return int.from_bytes(digest[:8], 'big') % (ceiling - batch_size + 1)


def _attribution(metadata, *, max_items=20):
    authors = metadata.get('authors', metadata.get('author', []))
    if isinstance(authors, str):
        authors = [authors]
    if not isinstance(authors, list):
        return []
    return [value[:200] for value in authors[:max_items]
            if isinstance(value, str) and value.strip()]


def record_for(dataset_id, entry, *, max_chars):
    config = SOURCES[dataset_id]
    row = entry.get('row')
    if not isinstance(row, dict):
        raise ValueError('invalid row payload')
    metadata = row.get('metadata')
    if not isinstance(metadata, dict):
        raise ValueError('source metadata absent')
    license_label = metadata.get('license')
    article_url = metadata.get('url')
    text = row.get('text')
    record_id = str(row.get('id') or '').strip()
    if (not isinstance(text, str) or len(text.strip()) < 80 or
            not isinstance(license_label, str) or not license_label.strip() or
            not isinstance(article_url, str) or not record_id):
        raise ValueError('text, source identifier, license or URL missing')
    parsed = urlparse(article_url)
    if parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password:
        raise ValueError('source attribution URL must be absolute HTTPS')
    content = text[:max_chars]
    if not canonical_text_key(content):
        raise ValueError('empty normalized text')
    return {
        'dataset_id': dataset_id,
        'dataset_revision_observed': config['revision'],
        'source_split': 'train',
        'record_id': record_id,
        'kind': 'text',  # Not a labeled retrieval triple.
        'content_category': config['kind'],
        'language': 'en',  # Publisher's declared language; not detected per row.
        'text': content,
        'was_truncated': len(text) > max_chars,
        'source_url': article_url,
        'source_group': str(row.get('source') or ''),
        'group_id': article_url,
        'created': str(row.get('created') or ''),
        'attribution_authors': _attribution(metadata),
        'source_license_claim': license_label,
        'training_approved': False,
        'privacy_review': 'pending',
        'source_rights_review': 'pending',
        'annotation_origin': 'upstream publication, not a query relevance judgment',
    }


def harvest(source_id, count, batch_size, max_chars):
    config = SOURCES[source_id]
    current = get_json(dataset_api_url(config['repo'])).get('sha')
    if current != config['revision']:
        raise ValueError(source_id + ': observed source HEAD does not match pinned audit')
    accepted, used_texts, used_urls = [], set(), set()
    stats = Counter()
    batches = (count + batch_size - 1) // batch_size
    for batch in range(batches):
        length = min(batch_size, count - batch * batch_size)
        offset = sampling_offset(source_id, batch, config['offset_ceiling'], length)
        response = get_json(server_url(
            'rows', dataset=config['repo'], config='default', split='train',
            offset=offset, length=length))
        rows = response.get('rows')
        if not isinstance(rows, list) or len(rows) != length:
            raise ValueError(source_id + ': incomplete dataset-server preview batch')
        for entry in rows:
            stats['examined'] += 1
            try:
                item = record_for(source_id, entry, max_chars=max_chars)
                fingerprint = canonical_text_key(item['text'])
                if fingerprint in used_texts or item['source_url'] in used_urls:
                    stats['duplicate_text_or_source_url'] += 1
                    continue
                used_texts.add(fingerprint)
                used_urls.add(item['source_url'])
                accepted.append(item)
                stats['accepted'] += 1
                if item['was_truncated']:
                    stats['truncated'] += 1
            except (TypeError, ValueError, KeyError):
                stats['invalid_or_missing_metadata'] += 1
    if get_json(dataset_api_url(config['repo'])).get('sha') != current:
        raise ValueError(source_id + ': upstream HEAD moved during preview; discard')
    if len(accepted) < count // 2:
        raise ValueError(source_id + ': insufficient candidate records to justify intake')
    return accepted, dict(sorted(stats.items()))


def prepare(out_dir, *, sources, count, batch_size, max_chars):
    if (not sources or len(set(sources)) != len(sources)
            or set(sources) - set(SOURCES)
            or not 2 <= count <= 500 or not 1 <= batch_size <= 10
            or not 500 <= max_chars <= 20000):
        raise ValueError('invalid source, count, batch or text-size cap')
    out_dir = Path(out_dir)
    if out_dir.exists():
        raise FileExistsError('refusing to replace an existing candidate directory')
    out_dir.mkdir(parents=True, mode=0o700, exist_ok=False)
    try:
        files, summaries = [], []
        for source in sources:
            rows, counts = harvest(source, count, batch_size, max_chars)
            dest = out_dir / (source + '.jsonl')
            with dest.open('w', encoding='utf-8') as stream:
                for item in rows:
                    stream.write(json.dumps(item, ensure_ascii=False, sort_keys=True) + '\n')
            os.chmod(dest, 0o600)
            digest = hashlib.sha256(dest.read_bytes()).hexdigest()
            files.append({'path': dest.name, 'sha256': digest, 'rows': len(rows)})
            summaries.append({
                'id': source,
                'repository': SOURCES[source]['repo'],
                'observed_head_revision': SOURCES[source]['revision'],
                'config': 'default', 'split': 'train',
                'sampling_offset_upper_bound_exclusive': SOURCES[source]['offset_ceiling'],
                'counts': counts, 'file': dest.name, 'sha256': digest,
                'license_review': 'pending', 'privacy_review': 'pending',
                'training_approved': False,
            })
        manifest = {
            'format': FORMAT, 'status': 'QUARANTINED: NOT APPROVED FOR TRAINING',
            'policy': POLICY, 'created_utc': datetime.now(timezone.utc).isoformat(),
            'max_chars': max_chars, 'count_per_source_requested': count,
            'sampling_batch_size': batch_size, 'files': files,
            'sources': summaries, 'training_approved': False,
            'limitations': [
                'Dataset Server cached preview rows are NOT pinned to the observed repository commit',
                'Stack Exchange preview only exposes an initial part of the upstream collection',
                'Hash-offset sample is not representative of all sources, languages or topics',
                'Rights and privacy must be reviewed per record, even when CC license is shown',
                'Exact text deduplication only; cross-corpus near duplicates and benchmarks unchecked',
                'No relevance labels, image bytes, video or audio',
            ],
        }
        manifest_path = out_dir / 'manifest.json'
        manifest_path.write_text(json.dumps(manifest, ensure_ascii=False,
                                            indent=2, sort_keys=True) + '\n', encoding='utf-8')
        os.chmod(manifest_path, 0o600)
        return manifest
    except BaseException:
        shutil.rmtree(out_dir)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out-dir', type=Path, required=True)
    parser.add_argument('--sources', nargs='+', choices=sorted(SOURCES),
                        default=sorted(SOURCES))
    parser.add_argument('--count-per-source', type=int, default=200)
    parser.add_argument('--batch-size', type=int, default=5)
    parser.add_argument('--max-chars', type=int, default=16000)
    args = parser.parse_args()
    report = prepare(args.out_dir, sources=args.sources,
                     count=args.count_per_source,
                     batch_size=args.batch_size, max_chars=args.max_chars)
    print(json.dumps({
        'format': report['format'], 'status': report['status'],
        'training_approved': False,
        'sources': [{k: source[k] for k in ('id', 'counts', 'observed_head_revision')}
                    for source in report['sources']],
    }, ensure_ascii=False, sort_keys=True))


if __name__ == '__main__':
    main()
