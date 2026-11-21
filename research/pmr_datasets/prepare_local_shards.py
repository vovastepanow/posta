#!/usr/bin/env python3
"""Create a bounded quarantine corpus from SHA-verified pinned local shards.

Requires pyarrow, Pillow and ffprobe. No network, PostgreSQL or training. Files
remain in quarantine until source rights, privacy and evaluation overlap review.
"""
import argparse
import hashlib
import io
import json
import math
import os
import subprocess
from collections import Counter
from pathlib import Path

import pyarrow.parquet as pq
from PIL import Image
from prepare import clean, normalize, partition, write_jsonl
from probe import atomic_json

SPECS = {
    'wikipedia-ru-20231101': ('text', 'ru', ['id', 'url', 'title', 'text']),
    'wikipedia-en-20231101': ('text', 'en', ['id', 'url', 'title', 'text']),
    'fineweb2-hq-ru': ('text', 'ru', ['text', 'id', 'url', 'language_score']),
    'fineweb2-ru': ('text', 'ru', ['text', 'id', 'url', 'language_score']),
    'fineweb-en': ('text', 'en', ['text', 'id', 'url', 'language_score']),
    'kalm-retrieval': ('retrieval', 'zh', ['query', 'pos', 'neg']),
    'librispeech': ('speech_text', 'en', ['audio', 'text', 'speaker_id', 'chapter_id', 'id']),
    'wit': ('image_text', 'und', ['image', 'image_url', 'metadata_url', 'wit_features']),
    'memexplain': ('meme_text', 'en', ['id', 'text', 'image', 'explanation_en', 'source_split']),
}


def file_hash(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b''):
            h.update(chunk)
    return h.hexdigest()


def load_sources(directory, selected=None):
    sources = {}
    for p in sorted(directory.glob('manifest-*.json')):
        data = json.loads(p.read_text(encoding='utf-8'))
        for id_, record in data['records'].items():
            if id_ in SPECS and (selected is None or id_ in selected) and record.get('status') in ('verified', 'verified_existing'):
                path = Path(record['local_path'])
                if not path.is_file() or path.stat().st_size != record['expected_bytes']:
                    raise ValueError(f'{id_}: missing file or size mismatch')
                if file_hash(path) != record['expected_sha256']:
                    raise ValueError(f'{id_}: file SHA256 mismatch')
                sources[id_] = record
    if selected and selected - sources.keys():
        raise ValueError('requested verified sources missing: ' + ', '.join(sorted(selected - sources.keys())))
    return sources


def media_file(data, kind, root):
    if not isinstance(data, bytes) or not data or len(data) > 16 * 1024 * 1024:
        raise ValueError('missing or oversized media bytes')
    if kind == 'speech_text':
        if not data.startswith(b'fLaC'):
            raise ValueError('audio is not FLAC')
        result = subprocess.run(['ffprobe', '-v', 'error', '-show_entries', 'stream=codec_name,sample_rate,channels',
                                 '-of', 'json', '-i', 'pipe:0'], input=data, capture_output=True, timeout=20)
        if result.returncode:
            raise ValueError('ffprobe failed to decode audio header')
        streams = json.loads(result.stdout).get('streams') or []
        if not streams or streams[0].get('codec_name') != 'flac':
            raise ValueError('unrecognized audio stream')
        suffix, info = '.flac', streams[0]
    else:
        with Image.open(io.BytesIO(data)) as im:
            if im.width * im.height > 40_000_000:
                raise ValueError('image exceeds pixel limit')
            im.load()
            fmt, info = im.format, {'width': im.width, 'height': im.height}
        suffix = {'JPEG': '.jpg', 'PNG': '.png', 'WEBP': '.webp'}.get(fmt)
        if not suffix:
            raise ValueError('unsupported image format')
    digest = hashlib.sha256(data).hexdigest()
    name = digest + suffix
    path = root / 'media' / name
    if not path.exists():
        path.write_bytes(data)
    elif file_hash(path) != digest:
        raise ValueError('existing media digest mismatch')
    return digest, {'path': 'media/' + name, 'sha256': digest, 'size_bytes': len(data), **info}


def convert(id_, raw, index, kind, lang, output):
    common = {'dataset_id': id_, 'record_id': str(raw.get('id') or index), 'kind': kind, 'language': lang}
    meta = {}
    if kind == 'text':
        value = raw.get('text')
        if id_.startswith('wikipedia-'):
            # Retain stable title and canonical article URL for attribution.
            title = clean(raw.get('title'))
            if not title or not isinstance(value, str) or not value.strip():
                raise ValueError('Wikipedia title/text missing')
            value = title + '\\n' + value
            meta.update(wikipedia_title=title,
                        upstream_license_claim='cc-by-sa-3.0; attribution review pending',
                        wikipedia_snapshot='20231101')
        common.update(text=value, group_id=str(raw.get('id') or index))
        meta.update(source_url=raw.get('url'), source_language_score=raw.get('language_score'))
    elif kind == 'retrieval':
        positives = raw.get('pos') or []
        if len(positives) != 1:
            raise ValueError('expected exactly one positive')
        common.update(text=raw.get('query'), positive=positives[0], negatives=raw.get('neg') or [], group_id=str(index))
        meta['annotation_origin'] = 'upstream AFQMC aggregate; source rights and benchmark overlap unverified'
    elif kind == 'speech_text':
        if len(clean(raw.get('text'))) < 4:
            raise ValueError('speech transcription too short')
        common.update(text=raw.get('text'), group_id='speaker:' + str(raw.get('speaker_id')))
        common['media_sha256'], meta['media'] = media_file((raw.get('audio') or {}).get('bytes'), kind, output)
        meta.update(speaker_id=raw.get('speaker_id'), chapter_id=raw.get('chapter_id'))
    elif kind == 'meme_text':
        if len(clean(raw.get('explanation_en'))) < 4:
            raise ValueError('meme explanation too short')
        common.update(text=raw.get('explanation_en'), ocr=raw.get('text'), group_id=str(raw.get('id') or index))
        common['media_sha256'], meta['media'] = media_file((raw.get('image') or {}).get('bytes'), kind, output)
        meta.update(ocr=raw.get('text'), upstream_source_split=raw.get('source_split'),
                    annotation_origin='upstream explanation; benchmark overlap unresolved')
    elif kind == 'image_text':
        features = raw.get('wit_features') or {}
        langs = features.get('language') or []
        reference = features.get('caption_reference_description') or []
        alt = features.get('caption_alt_text_description') or []
        selected = None
        for wanted in ('ru', 'en'):
            for j, language in enumerate(langs):
                if language == wanted:
                    caption = (reference[j] if j < len(reference) else None) or (alt[j] if j < len(alt) else None)
                    if isinstance(caption, str) and len(caption.strip()) >= 4:
                        selected = (wanted, caption)
                        break
            if selected:
                break
        if not selected:
            raise ValueError('no Russian or English caption')
        common.update(language=selected[0], text=selected[1], group_id=str(raw.get('image_url') or index))
        common['media_sha256'], meta['media'] = media_file((raw.get('image') or {}).get('bytes'), kind, output)
        meta.update(source_url=raw.get('image_url'), metadata_url=raw.get('metadata_url'),
                    annotation_origin='WIT upstream caption; individual media license unverified')
    return normalize(common), meta



def split_without_negative_leakage(rows):
    # Anchor queries, positives, groups and media first. Shared negatives are then
    # retained in only one split, rather than joining the entire graph together.
    anchors = [dict(row, negatives=[]) for row in rows]
    for anchor in anchors:
        anchor['_fingerprint'] = normalize(anchor)['_fingerprint']
    assignments = partition(anchors)
    anchor_splits = {}
    for i, row in enumerate(rows):
        for content in [row['text'], row['positive'], *row.get('_auxiliary_texts', [])]:
            if content:
                anchor_splits[clean(content).casefold()] = assignments[i]
    negative_owners = {}
    for i, row in enumerate(rows):
        for content in row['negatives']:
            key = clean(content).casefold()
            candidate = (anchors[i]['_fingerprint'], assignments[i])
            if key not in negative_owners or candidate < negative_owners[key]:
                negative_owners[key] = candidate
    dropped = 0
    for i, row in enumerate(rows):
        retained = []
        for content in row['negatives']:
            key = clean(content).casefold()
            owner = anchor_splits.get(key, negative_owners[key][1])
            if owner == assignments[i]:
                retained.append(content)
            else:
                dropped += 1
        row['negatives'] = retained
        row['_fingerprint'] = normalize(row)['_fingerprint']
    return assignments, dropped



def ensure_speech_holdouts(rows, assignments):
    # Move an isolated speaker component only when a split is missing. Never
    # separate a shared text/media identity or take the last train speaker.
    groups = {}
    for i, row in enumerate(rows):
        if row['kind'] == 'speech_text':
            groups.setdefault((row['dataset_id'], row['group_id']), []).append(i)
    if len(groups) < 3:
        return []
    moved = []
    for missing in ('validation', 'test'):
        if any(assignments[i] == missing for indexes in groups.values() for i in indexes):
            continue
        train_groups = [key for key, indexes in groups.items() if all(assignments[i] == 'train' for i in indexes)]
        if len(train_groups) <= 1:
            continue
        for group in sorted(train_groups):
            selected = set(groups[group]); texts = set(); media = set(); urls = set()
            for i in selected:
                row = rows[i]
                texts.update(clean(t).casefold() for t in [row['text'], row['positive'], *row.get('_auxiliary_texts', []), *row['negatives']] if clean(t))
                if row['media_sha256']:
                    media.add(row['media_sha256'])
                if row.get('_source_url'):
                    urls.add(row['_source_url'])
            isolated = True
            for i, row in enumerate(rows):
                if i in selected:
                    continue
                if ((row['dataset_id'], row['group_id']) == group or
                    row['media_sha256'] and row['media_sha256'] in media or
                    row.get('_source_url') and row['_source_url'] in urls or
                    any(clean(t).casefold() in texts for t in [row['text'], row['positive'], *row.get('_auxiliary_texts', []), *row['negatives']] if clean(t))):
                    isolated = False
                    break
            if isolated:
                for i in selected:
                    assignments[i] = missing
                moved.append({'dataset_id': group[0], 'group_id': group[1], 'split': missing, 'rows': len(selected)})
                break
    return moved


def source_rows(reader, columns, kind, limit):
    if kind != 'speech_text':
        index = 0
        for batch in reader.iter_batches(batch_size=8, columns=columns, use_threads=False):
            for raw in batch.to_pylist():
                yield index, raw
                index += 1
        return
    # Spread the sample across row groups rather than taking the first speaker.
    per_group = max(1, math.ceil(limit / reader.num_row_groups))
    base = 0
    for group in range(reader.num_row_groups):
        local = 0
        for batch in reader.iter_batches(batch_size=8, columns=columns, row_groups=[group], use_threads=False):
            for raw in batch.to_pylist():
                yield base + local, raw
                local += 1
                if local >= per_group:
                    break
            if local >= per_group:
                break
        base += reader.metadata.row_group(group).num_rows


def build(source_dir, output, limits, selected=None):
    sources = load_sources(source_dir, selected)
    if not sources:
        raise ValueError('no verified supported source files')
    output.mkdir(parents=True, exist_ok=False)
    (output / 'media').mkdir()
    rows, metas, problems, examined = [], [], Counter(), Counter()
    for id_, record in sorted(sources.items()):
        kind, language, columns = SPECS[id_]
        count = 0
        reader = pq.ParquetFile(record['local_path'])
        for index, raw in source_rows(reader, columns, kind, limits[id_]):
            examined[id_] += 1
            try:
                row, meta = convert(id_, raw, index, kind, language, output)
                if clean(meta.get('source_url')):
                    row['_source_url'] = clean(meta['source_url'])
                rows.append(row)
                meta.update(dataset_revision=record['revision'], source_file_sha256=record['expected_sha256'],
                            source_file=record['source_path'], source_row_index=index,
                            training_approved=False)
                metas.append(meta)
                count += 1
            except (ValueError, OSError, subprocess.SubprocessError) as exc:
                problems[f'{id_}: {str(exc)[:120]}'] += 1
            if count >= limits[id_] or examined[id_] >= limits[id_] * 20:
                break
    assignments, negative_drops = split_without_negative_leakage(rows)
    speech_holdouts = ensure_speech_holdouts(rows, assignments)
    seen, grouped, duplicates = set(), {}, 0
    for i, row in enumerate(rows):
        if row['_fingerprint'] in seen:
            duplicates += 1
            continue
        seen.add(row['_fingerprint'])
        clean_row = {k: v for k, v in row.items() if not k.startswith('_')}
        clean_row.update(metas[i], split=assignments[i])
        grouped.setdefault((row['kind'], assignments[i]), []).append(clean_row)
    files, counts = [], Counter()
    for (kind, split), batch in sorted(grouped.items()):
        path = output / f'{kind}-{split}.jsonl'
        write_jsonl(path, batch)
        files.append({'path': path.name, 'sha256': file_hash(path), 'rows': len(batch)})
        for row in batch:
            counts[(kind, split, row['language'])] += 1
    manifest = {'version': 'posta-dataset-v0.1-candidate',
                'status': 'QUARANTINED: not cleared or benchmark-screened for training',
                'sources': {id_: {k: r[k] for k in ('repository', 'revision', 'source_path', 'expected_sha256', 'expected_bytes')} for id_, r in sources.items()},
                'files': files, 'counts': [{'kind': k, 'split': s, 'language': l, 'rows': n} for (k, s, l), n in sorted(counts.items())],
                'accepted': sum(counts.values()), 'exact_duplicates_removed': duplicates,
                'negatives_removed_to_prevent_cross_split_overlap': negative_drops,
                'speaker_holdouts_created': speech_holdouts,
                'examined': dict(examined), 'rejections': dict(problems),
                'limitations': ['All records require source rights and privacy review',
                                'No independent benchmark overlap or translated/near-duplicate screening',
                                'MemeXplain upstream source_split empty; possible benchmark overlap',
                                'Audio grouped by speaker, not checked across other corpora',
                                'Negatives shared across splits were removed to avoid leakage',
                                'Split assignment may change when a connected component changes',
                                'Video and OCR not yet included; no model training performed']}
    atomic_json(output / 'manifest.json', manifest)
    return manifest


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source-dir', type=Path, required=True)
    p.add_argument('--out-dir', type=Path, required=True)
    p.add_argument('--ids', nargs='+', choices=sorted(SPECS), help='restrict to selected verified sources')
    p.add_argument('--text', type=int, default=1000)
    p.add_argument('--retrieval', type=int, default=1000)
    p.add_argument('--media', type=int, default=100)
    args = p.parse_args()
    if not (1 <= args.text <= 20000 and 1 <= args.retrieval <= 5000 and 1 <= args.media <= 5000):
        p.error('limits: text 1..20000, retrieval 1..5000, media 1..5000')
    limits = {k: args.text if v[0] == 'text' else args.retrieval if v[0] == 'retrieval' else args.media for k,v in SPECS.items()}
    result = build(args.source_dir, args.out_dir, limits, set(args.ids) if args.ids else None)
    print(json.dumps({'accepted': result['accepted'], 'counts': result['counts'],
                      'rejections': result['rejections']}, ensure_ascii=False))


if __name__ == '__main__':
    main()
