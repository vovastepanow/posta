"""Deterministic, fail-closed preparation for *future* retrieval pilot samples.

This leaves the historical v1/v9 samplers unchanged. It does not authorize
training on a quarantined dataset, write data, or leak input text in logs.
"""
import hashlib
import json
import unicodedata


CURATION_POLICY = 'posta-retrieval-curated-v1'
RUN001_CURATED_POLICY = 'posta-retrieval-curated-strict-holdout-v2'


def normalized(value):
    if not isinstance(value, str):
        return ''
    return ' '.join(unicodedata.normalize('NFKC', value).casefold().split())


def _hash_bytes(text):
    return hashlib.sha256(text.encode('utf-8')).digest()


def _triple_order(triple):
    return (hashlib.sha256(json.dumps(triple, ensure_ascii=False,
                separators=(',', ':')).encode('utf-8')).digest(), triple)


def curate_retrieval_pairs(corpus, limit, split='train', avoid_heldout=True,
                           strict_heldout_negatives=False):
    """Select distinct normalized queries with consistent, explicit negatives.

    A negative is rejected if it matches *any* known positive for the same
    normalized query, not just the positive in its own row. For train split,
    optionally exclude query/positive text present in validation or test.
    These are exact normalized checks, not semantic-equivalence guarantees.
    """
    if type(limit) is not int or limit < 1:
        raise ValueError('limit must be a positive integer')
    if split not in ('train', 'validation', 'test'):
        raise ValueError('unsupported split')
    if avoid_heldout and split != 'train':
        raise ValueError('held-out exclusion is only supported for training')
    if strict_heldout_negatives and (split != 'train' or not avoid_heldout):
        raise ValueError('strict held-out negative exclusion requires train split')

    forbidden = set()
    if avoid_heldout:
        for partition in ('validation', 'test'):
            for item in corpus.rows('retrieval', partition):
                for field in ('text', 'positive'):
                    text = normalized(item.get(field))
                    if text:
                        forbidden.add(text)
                if strict_heldout_negatives:
                    for negative in item.get('negatives') or []:
                        text = normalized(negative)
                        if text:
                            forbidden.add(text)

    data = []
    known_positives = {}
    stats = {
        'total_rows': 0,
        'invalid_query_or_positive': 0,
        'rows_without_usable_negative': 0,
        'negative_matches_known_positive': 0,
        'negative_matches_query': 0,
        'rows_excluded_heldout_text': 0,
        'negatives_excluded_heldout_text': 0,
        'eligible_rows': 0,
        'eligible_distinct_queries': 0,
        'selected_pairs': 0,
    }
    for row in corpus.rows('retrieval', split):
        stats['total_rows'] += 1
        query = normalized(row.get('text'))
        positive = normalized(row.get('positive'))
        if not query or not positive:
            stats['invalid_query_or_positive'] += 1
            continue
        known_positives.setdefault(query, set()).add(positive)
        data.append((row, query, positive))

    choices = {}
    for row, query, positive in data:
        if avoid_heldout and (query in forbidden or positive in forbidden):
            stats['rows_excluded_heldout_text'] += 1
            continue

        raw = row.get('negatives')
        if not isinstance(raw, list):
            raw = []
        valid = {}
        for candidate in raw:
            candidate_norm = normalized(candidate)
            if not candidate_norm:
                continue
            if candidate_norm == query:
                stats['negative_matches_query'] += 1
                continue
            if strict_heldout_negatives and candidate_norm in forbidden:
                stats['negatives_excluded_heldout_text'] += 1
                continue
            if candidate_norm in known_positives[query]:
                stats['negative_matches_known_positive'] += 1
                continue
            # Stable representative independent of row or candidate order.
            value = candidate.strip()
            if candidate_norm not in valid or value < valid[candidate_norm]:
                valid[candidate_norm] = value
        if not valid:
            stats['rows_without_usable_negative'] += 1
            continue

        stats['eligible_rows'] += 1
        q = row['text'].strip()
        p = row['positive'].strip()
        # A deterministic choice among explicit negatives avoids source-list
        # order bias while preserving the original useful text.
        neg = min(valid.values(), key=lambda s: (_hash_bytes(normalized(s)), s))
        triple = (q, p, neg)
        if query not in choices or _triple_order(triple) < _triple_order(choices[query]):
            choices[query] = triple

    stats['eligible_distinct_queries'] = len(choices)
    if len(choices) < limit:
        raise ValueError(
            f'not enough curated queries: available={len(choices)}, requested={limit}')
    ranked = sorted(choices, key=lambda key: (_hash_bytes(key), key))
    selected = [choices[key] for key in ranked[:limit]]
    stats['selected_pairs'] = len(selected)

    policy = RUN001_CURATED_POLICY if strict_heldout_negatives else CURATION_POLICY
    digest = hashlib.sha256(policy.encode('ascii'))
    for triple in selected:
        data = json.dumps(triple, ensure_ascii=False, separators=(',', ':')).encode('utf-8')
        digest.update(len(data).to_bytes(8, 'big'))
        digest.update(data)
    return selected, digest.hexdigest(), stats
