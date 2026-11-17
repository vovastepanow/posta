#!/usr/bin/env python3
"""Export blind cases and join independently produced labels and decisions.

Inputs and outputs are explicit; no file with annotations is ever overwritten.
A case is identified by (profile, sample_id), because a post may appear under
more than one profile. Missing observations remain unknown.
"""
import argparse
import copy
import json
from pathlib import Path
from urllib.parse import urlparse


def case_key(item):
    return item['profile'], item['sample_id']


def index_cases(data):
    cases = {}
    for case in data['samples']:
        key = case_key(case)
        if key in cases:
            raise ValueError('duplicate profile/sample_id')
        cases[key] = case
    return cases


def blind(data, fixture_root=None):
    index_cases(data)
    samples = []
    for case in data['samples']:
        row = {k: case[k] for k in ('profile', 'sample_id', 'text', 'posted_at', 'media_kinds')}
        if fixture_root is not None:
            name = case['fixture']
            if name not in ('source_preview_rss.json', 'source_preview_tg_text.json', 'source_preview_tg_memes.json'):
                raise ValueError('untrusted fixture name')
            fixture = json.loads((fixture_root / name).read_text(encoding='utf-8'))
            posts = fixture['discovered']['posts']
            index = case['fixture_index']
            if type(index) is not int or not 0 <= index < len(posts):
                raise ValueError('invalid fixture index')
            if posts[index].get('text', '') != case['text'] or posts[index].get('posted_at') != case['posted_at']:
                raise ValueError('fixture content changed since the sample was prepared')
            row['media'] = []
            for media in posts[index].get('media', []):
                ref = {'kind': media.get('kind', '')}
                for field in ('src', 'poster', 'page'):
                    raw = media.get(field) or ''
                    p = urlparse(raw)
                    if p.scheme == 'https' and p.hostname and not p.username and not p.password and not p.query and not p.fragment:
                        ref[field] = raw
                row['media'].append(ref)
        samples.append(row)
    return {'purpose': 'independent blind annotation', 'samples': samples}


def load_records(path):
    records = json.loads(Path(path).read_text(encoding='utf-8'))
    if not isinstance(records, list):
        raise ValueError('input must be an array')
    return records


def merge(data, annotations, baseline, auto):
    result = copy.deepcopy(data)
    cases = index_cases(result)
    for rows, kind in ((annotations, 'annotation'), (baseline, 'baseline'), (auto, 'auto')):
        seen = set()
        for row in rows:
            key = case_key(row)
            if key not in cases or key in seen:
                raise ValueError('unknown or duplicate case')
            seen.add(key)
            if kind == 'annotation':
                label = row.get('annotation')
                if not isinstance(label, dict) or not label or any(k not in ('relevant', 'duplicate', 'advertising') or type(v) is not bool for k, v in label.items()):
                    raise ValueError('invalid annotation')
                if cases[key].get('annotation') not in (None, label):
                    raise ValueError('conflicting annotation')
                cases[key]['annotation'] = copy.deepcopy(label)
            else:
                decision = row.get('decision')
                if type(decision) is not bool:
                    raise ValueError('invalid decision')
                existing = cases[key].setdefault('decisions', {})
                if kind in existing and existing[kind] is not decision:
                    raise ValueError('conflicting decision')
                existing[kind] = decision
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('blind', 'merge'))
    parser.add_argument('--sample', required=True)
    parser.add_argument('--output', required=True)
    parser.add_argument('--annotations')
    parser.add_argument('--baseline')
    parser.add_argument('--auto')
    args = parser.parse_args()
    data = json.loads(Path(args.sample).read_text(encoding='utf-8'))
    if args.mode == 'blind':
        if args.annotations or args.baseline or args.auto:
            parser.error('blind does not take result files')
        output = blind(data, Path(args.sample).resolve().parent)
    else:
        files = [args.annotations, args.baseline, args.auto]
        output = merge(data, *(load_records(path) if path else [] for path in files))
    with Path(args.output).open('x', encoding='utf-8') as f:
        json.dump(output, f, ensure_ascii=False, indent=2)
        f.write('\n')


if __name__ == '__main__':
    main()
