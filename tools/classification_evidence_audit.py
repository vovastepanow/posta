#!/usr/bin/env python3
"""Offline provenance audit of archived Responses API classification requests.

Never sends requests, recovers malformed answers, or assigns/removes labels.
Literal evidence is not semantic proof; even a clean row needs subject review.
"""
import argparse
import hashlib
import json
import re
from pathlib import Path


def normalized(text):
    return ' '.join(text.lower().replace('ё', 'е').split())


def evidence_status(source, quote):
    if not isinstance(source, str) or not isinstance(quote, str) or not quote.strip():
        return 'missing_source_or_quote'
    source, quote = normalized(source), normalized(quote)
    if quote not in source:
        return 'not_in_source'
    stripped = normalized(re.sub(r'(^|[^\w])([#@]\w+|https?://\S+)', r'\1 ', source))
    body_quote = normalized(re.sub(r'(^|[^\w])([#@]\w+|https?://\S+)', r'\1 ', quote))
    if len(body_quote) < 4 or body_quote not in stripped:
        return 'metadata_only'
    return 'literal_match_semantics_unchecked'


def audit(request, responses):
    text = request['input'][0]['content'][0]['text']
    records = json.loads(text.split('Публикации:\n', 1)[1])
    by_id = {r['id']: r for r in records}
    if len(by_id) != len(records):
        raise ValueError('Duplicate input records')
    trials = []
    for response in responses:
        trial = {'number': response['number'], 'records_expected': len(records), 'rows': []}
        raw = response.get('text')
        if not isinstance(raw, str) or not raw.strip():
            trial['status'] = 'no_response_text'
            trial['records_unassessed'] = list(by_id)
            trials.append(trial)
            continue
        try:
            answer = json.loads(raw)
            items = answer['items']
            if not isinstance(items, list) or any(not isinstance(i, dict) for i in items):
                raise ValueError('Invalid items')
            ids = [i['id'] for i in items]
            if len(ids) != len(set(ids)) or set(ids) - by_id.keys():
                raise ValueError('Duplicate/unknown result ids')
        except (ValueError, TypeError, KeyError):
            trial['status'] = 'invalid_envelope'
            trial['records_unassessed'] = list(by_id)
            trials.append(trial)
            continue
        trial['status'] = 'parsed_for_audit_only'
        trial['records_unassessed'] = sorted(by_id.keys() - set(ids))
        for item in items:
            record = by_id[item['id']]
            evidence = item.get('evidence', [])
            for tag in item.get('topics', []):
                quotes = [e for e in evidence if isinstance(e, dict) and e.get('tag') == tag]
                row = {'id': item['id'], 'tag': tag, 'automatic_application_allowed': False}
                if len(quotes) != 1:
                    row['evidence_status'] = 'missing_or_duplicate_evidence'
                else:
                    e = quotes[0]
                    source = e.get('source')
                    row['source'] = source
                    row['quote'] = e.get('quote')
                    row['evidence_status'] = evidence_status(record.get(source) if source in ('full_text', 'meaning_description') else None, e.get('quote'))
                trial['rows'].append(row)
        trials.append(trial)
    return {'mode': 'offline_diagnostic_only', 'cloud_calls': 0, 'cost_usd': 0,
            'unique_records': len(records), 'trials': trials}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--request', type=Path, required=True)
    parser.add_argument('--responses', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    result = audit(json.loads(args.request.read_text()), [json.loads(s) for s in args.responses.read_text().splitlines() if s.strip()])
    result['input_sha256'] = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in (args.request, args.responses)}
    # Create private output before writing potentially sensitive quotations.
    import os
    fd = os.open(args.output, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, 'w') as f:
        json.dump(result, f, ensure_ascii=False, indent=2)
        f.write('\n')


if __name__ == '__main__':
    main()
