"""Deterministic, split-isolated local media pairs for research only.

Captions describe the video weakly; speech transcripts describe speech. They
are different labels and must not be treated as captions of all sounds.
Exact overlap checks cannot detect paraphrases or related source recordings.
"""
import hashlib
import json
import math

from retrieval_data_quality import normalized


def _identity(row):
    dataset = row.get('dataset_id')
    group = row.get('group_id')
    media = (row.get('media') or {}).get('sha256')
    text = normalized(row.get('text'))
    if not all(isinstance(x, str) and x for x in (dataset, group, media, text)) or len(media) != 64:
        return None
    return (dataset, group), media, text


def select_media_pairs(corpus, kind, split, limit):
    if kind not in ('video_text', 'speech_text') or split not in ('train', 'validation'):
        raise ValueError('invalid media pair split or kind')
    if type(limit) is not int or limit < 2:
        raise ValueError('at least two media pairs required')
    other = 'validation' if split == 'train' else 'train'
    forbidden = [_identity(row) for row in corpus.rows(kind, other)]
    forbidden = [item for item in forbidden if item is not None]
    groups = {x[0] for x in forbidden}
    media = {x[1] for x in forbidden}
    texts = {x[2] for x in forbidden}
    candidates = []
    for row in corpus.rows(kind, split):
        identity = _identity(row)
        if identity is None or identity[0] in groups or identity[1] in media or identity[2] in texts:
            continue
        if row.get('language') != 'en':
            continue
        if kind == 'video_text':
            try:
                duration = float(row.get('duration_seconds') or 0)
            except (TypeError, ValueError):
                continue
            if not math.isfinite(duration) or not 1 <= duration <= 8:
                continue
        raw = json.dumps([row.get('record_id'), identity], ensure_ascii=False,
                         separators=(',', ':')).encode('utf-8')
        candidates.append((hashlib.sha256(raw).digest(), identity, row))
    selected = []
    used_groups, used_media, used_text = set(), set(), set()
    for _, (group, media_sha, text), row in sorted(candidates, key=lambda x: x[0]):
        # Speech may have many distinct utterances from one held-out speaker;
        # retain them but do not describe them as independent speakers.
        if (kind == 'video_text' and group in used_groups) or media_sha in used_media or text in used_text:
            continue
        selected.append(row)
        used_groups.add(group); used_media.add(media_sha); used_text.add(text)
        if len(selected) == limit:
            break
    if len(selected) != limit:
        raise ValueError(f'only {len(selected)} distinct isolated {kind} {split} records available')
    digest = hashlib.sha256(b'posta-av-pairs-v1')
    for row in selected:
        raw = json.dumps([row['record_id'], row['media']['sha256'], normalized(row['text'])],
                         ensure_ascii=False, separators=(',', ':')).encode('utf-8')
        digest.update(len(raw).to_bytes(8, 'big')); digest.update(raw)
    return selected, digest.hexdigest()
