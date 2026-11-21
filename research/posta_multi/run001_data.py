"""Posta Multi Run 001: train-only, reproducible multilingual MLM windows.

The stream never uses the test split, and never writes raw examples to output
reports. Each (modality, language) is independently hash-sampled to avoid
selecting only the first corpus rows. The tokenization is versioned separately.
"""
import hashlib
import json
from collections import Counter

KINDS = ('text', 'retrieval', 'ocr_text', 'image_text',
         'meme_text', 'video_text', 'speech_text')
POLICY = 'posta-run001-balanced-multimodal-text-v1'


def text_fields(kind, row):
    """Text is always present in the Corpus; retrieval has one extra positive."""
    text = row.get('text')
    if isinstance(text, str) and text.strip():
        yield text
    if kind == 'retrieval':
        positive = row.get('positive')
        if isinstance(positive, str) and positive.strip():
            yield positive
    if kind == 'ocr_text':
        ocr = row.get('ocr')
        if isinstance(ocr, str) and ocr.strip() and ocr != text:
            yield ocr


def select_records(corpus, *, split, max_per_group, max_chars=3072):
    if split not in ('train', 'validation'):
        raise ValueError('Run 001 samples must be train or validation only')
    if type(max_per_group) is not int or max_per_group < 1 or max_chars < 32:
        raise ValueError('invalid selection budget')
    rows = {}
    counts = Counter()
    for kind in KINDS:
        for row in corpus.rows(kind, split):
            lang = row.get('language') or 'und'
            if not isinstance(lang, str):
                lang = 'und'
            group = (kind, lang)
            for text in text_fields(kind, row):
                clipped = text[:max_chars]
                if not clipped.strip():
                    continue
                digest = hashlib.sha256(clipped.encode('utf-8')).hexdigest()
                key = (digest, clipped)
                rows.setdefault(group, {})[key] = clipped
                counts[group] += 1
    selected = []
    summary = {}
    for group in sorted(rows):
        chosen = sorted(rows[group])[:max_per_group]
        selected.extend((group, rows[group][key]) for key in chosen)
        summary[f'{group[0]}:{group[1]}'] = {
            'considered': counts[group],
            'unique': len(rows[group]),
            'selected': len(chosen),
        }
    if not selected:
        raise ValueError(f'no eligible {split} text records')
    # Interleave kinds/languages deterministically rather than exposing a
    # fixed content order to the training sampler.
    selected.sort(key=lambda item: (hashlib.sha256(item[1].encode('utf-8')).digest(), item[0]))
    return selected, summary


def make_windows(selected, tokenizer, length, max_windows_per_record=4):
    if length < 8 or max_windows_per_record < 1:
        raise ValueError('invalid tokenization window budget')
    results = []
    digester = hashlib.sha256(POLICY.encode('utf-8'))
    lengths = Counter()
    for group, content in selected:
        token_ids = tokenizer.encode(content, add_special_tokens=False).ids
        if not token_ids:
            continue
        for start in range(0, len(token_ids), length-2):
            if start // (length-2) >= max_windows_per_record:
                break
            payload = tuple([1, *token_ids[start:start+length-2], 2])
            if len(payload) < 4:
                continue
            serialized = json.dumps(payload, separators=(',', ':')).encode()
            digester.update(len(serialized).to_bytes(8, 'big'))
            digester.update(serialized)
            results.append(payload)
            lengths[f'{group[0]}:{group[1]}'] += 1
    if not results:
        raise ValueError('no nonempty token windows')
    return results, digester.hexdigest(), dict(sorted(lengths.items()))


def approval_issues(manifest):
    """No auto-approval based on a manifest existence or noncommercial intent."""
    if not isinstance(manifest, dict):
        return ['invalid_manifest']
    required = {
        'status': 'approved_for_training',
        'privacy_review': 'passed',
        'source_rights_review': 'passed',
    }
    return [f'{field}_not_{expected}' for field, expected in required.items()
            if manifest.get(field) != expected]
