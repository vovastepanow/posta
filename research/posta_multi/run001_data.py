"""Posta Multi Run 001: train-only, reproducible multilingual MLM windows.

The stream never uses the test split, and never writes raw examples to output
reports. Each (modality, language) is independently hash-sampled to avoid
selecting only the first corpus rows. The tokenization is versioned separately.
"""
import hashlib
import heapq
import json
from collections import Counter
from dataclasses import dataclass

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



@dataclass(frozen=True)
class _RankedText:
    digest: bytes
    text: str

    def __lt__(self, other):
        # Reverse normal order so heap[0] is the WORST (largest) record.
        return (self.digest, self.text) > (other.digest, other.text)


class _BoundedHashSample:
    """Top-K lowest SHA-256 text digests with at most K texts resident.

    This produces the same selected texts as globally sorting every
    (digest,text) pair, without O(total-corpus-size) memory.
    """
    def __init__(self, limit):
        self.limit = limit
        self.heap = []
        self.members = set()

    def add(self, text):
        digest = hashlib.sha256(text.encode('utf-8')).digest()
        key = (digest, text)
        if key in self.members:
            return
        candidate = _RankedText(digest, text)
        if len(self.heap) < self.limit:
            heapq.heappush(self.heap, candidate)
            self.members.add(key)
        elif key < (self.heap[0].digest, self.heap[0].text):
            old = heapq.heapreplace(self.heap, candidate)
            self.members.remove((old.digest, old.text))
            self.members.add(key)

    def selected(self):
        return [(x.digest, x.text) for x in sorted(
            self.heap, key=lambda x: (x.digest, x.text))]


def select_records(corpus, *, split, max_per_group, max_chars=3072):
    if split not in ('train', 'validation'):
        raise ValueError('Run 001 samples must be train or validation only')
    if type(max_per_group) is not int or max_per_group < 1 or max_chars < 32:
        raise ValueError('invalid selection budget')
    samples = {}
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
                if group not in samples:
                    samples[group] = _BoundedHashSample(max_per_group)
                samples[group].add(clipped)
                counts[group] += 1
    selected = []
    summary = {}
    for group in sorted(samples):
        chosen = samples[group].selected()
        selected.extend((group, text) for _, text in chosen)
        summary[f'{group[0]}:{group[1]}'] = {
            'considered': counts[group],
            'retained_unique': len(chosen),
            'selected': len(chosen),
            'global_unique_count_known': False,
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


def balanced_validation_windows(selected, tokenizer, length, per_group=4,
                                max_windows_per_record=4):
    """Deterministic token windows balanced across (kind, language) groups."""
    if type(per_group) is not int or per_group < 1:
        raise ValueError('per_group must be positive')
    if length < 8 or max_windows_per_record < 1:
        raise ValueError('invalid validation window dimensions')
    grouped = {}
    for group, text in selected:
        ids = tokenizer.encode(text, add_special_tokens=False).ids
        for start in range(0, len(ids), length - 2):
            if start // (length-2) >= max_windows_per_record:
                break
            window = tuple([1, *ids[start:start+length-2], 2])
            if len(window) >= 4:
                grouped.setdefault(group, set()).add(window)
    if not grouped:
        raise ValueError('no validation token windows')
    windows, labels, group_counts = [], [], {}
    digest = hashlib.sha256((POLICY + '-stratified-validation-v1').encode())
    for group in sorted(grouped):
        chosen = sorted(grouped[group], key=lambda window: (
            hashlib.sha256(json.dumps(window, separators=(',', ':')).encode()).digest(),
            window))[:per_group]
        label = f'{group[0]}:{group[1]}'
        group_counts[label] = len(chosen)
        for window in chosen:
            windows.append(window)
            labels.append(label)
            encoded = json.dumps([label, window], separators=(',', ':')).encode()
            digest.update(len(encoded).to_bytes(8, 'big'))
            digest.update(encoded)
    if len(windows) < 2:
        raise ValueError('too few validation windows')
    return windows, labels, digest.hexdigest(), group_counts



def cross_split_exact_overlaps(train_selected, validation_selected):
    """Number of distinct normalized texts shared across train and validation.

    Compare text across ALL kinds/languages (not merely corresponding
    groups). Return aggregate count only; no raw texts or hashes are exposed.
    """
    import unicodedata

    def token(text):
        normalized = ' '.join(unicodedata.normalize('NFKC', text).casefold().split())
        return hashlib.sha256(normalized.encode('utf-8')).digest() if normalized else None

    seen = {fp for _, text in train_selected if (fp := token(text))}
    return len(seen & {fp for _, text in validation_selected if (fp := token(text))})


def require_isolated_splits(train_selected, validation_selected):
    """Fail before allocating 235M weights if exact train/validation reuse exists."""
    count = cross_split_exact_overlaps(train_selected, validation_selected)
    if count:
        raise ValueError(
            f'train and validation share {count} distinct normalized text records; '
            'regenerate split-isolated dataset before training')
    return 0


def require_full_heldout_isolation(corpus, train_selected, max_chars=3072):
    """Screen selected train text against all validation/test text fields."""
    results = {}
    for split in ('validation', 'test'):
        heldout = []
        for kind in KINDS:
            for row in corpus.rows(kind, split):
                group = (kind, row.get('language') or 'und')
                heldout.extend((group, text[:max_chars]) for text in text_fields(kind, row))
        count = cross_split_exact_overlaps(train_selected, heldout)
        if count:
            raise ValueError(f'train and {split} share {count} normalized text records')
        results[split] = 0
    return results
