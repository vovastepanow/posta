#!/usr/bin/env python3
"""Join a bounded sample of pinned mMARCO train IDs, queries and passages.

Outputs inspection-only JSONL and a source manifest. No validation/test files,
network access, PostgreSQL access, model training, or license acceptance.
"""
import argparse
import hashlib
import json
from pathlib import Path
from probe import atomic_json

IDS = ('mmarco-ru', 'mmarco-ru-queries', 'mmarco-train-ids')


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def verified_sources(root):
    records = {}
    for manifest in sorted(root.glob('manifest-*.json')):
        for id_, row in json.loads(manifest.read_text())['records'].items():
            if id_ in IDS and row.get('status') in ('verified', 'verified_existing'):
                file = Path(row['local_path'])
                if not file.is_file() or file.stat().st_size != row['expected_bytes'] or digest(file) != row['expected_sha256']:
                    raise ValueError(f'{id_}: size or SHA256 mismatch')
                records[id_] = row
    missing = set(IDS) - records.keys()
    if missing:
        raise ValueError('missing fully verified inputs: ' + ', '.join(sorted(missing)))
    if len({r['revision'] for r in records.values()}) != 1:
        raise ValueError('mMARCO inputs have different revisions')
    return records


def read_ids(path, limit):
    triples = []
    with path.open(encoding='utf-8') as f:
        for row_num, line in enumerate(f):
            parts = line.rstrip('\r\n').split('\t')
            if len(parts) != 3 or not all(x.isdigit() for x in parts):
                raise ValueError(f'invalid triple row {row_num}')
            triples.append((row_num, *parts))
            if len(triples) == limit:
                break
    return triples


def read_selected(path, wanted):
    selected = {}
    with path.open(encoding='utf-8') as f:
        for row_num, line in enumerate(f):
            identifier, sep, text = line.rstrip('\r\n').partition('\t')
            if not sep:
                raise ValueError(f'invalid TSV row {row_num}')
            if identifier in wanted:
                if identifier in selected:
                    raise ValueError(f'duplicate source ID: {identifier}')
                selected[identifier] = text
                if len(selected) == len(wanted):
                    break
    return selected


def join(root, out, limit):
    sources = verified_sources(root)
    triples = read_ids(Path(sources['mmarco-train-ids']['local_path']), limit)
    if not triples:
        raise ValueError('no triples')
    qids = {x[1] for x in triples}
    pids = {x[2] for x in triples} | {x[3] for x in triples}
    queries = read_selected(Path(sources['mmarco-ru-queries']['local_path']), qids)
    passages = read_selected(Path(sources['mmarco-ru']['local_path']), pids)
    out.mkdir(parents=True, exist_ok=False)
    output = out / 'mmarco-ru-source.jsonl'
    emitted, missing = 0, 0
    with output.open('w', encoding='utf-8') as f:
        for index, query, positive, negative in triples:
            if query not in queries or positive not in passages or negative not in passages:
                missing += 1
                continue
            row = {'dataset_id':'mmarco-ru', 'record_id':str(index), 'kind':'retrieval',
                   'language':'ru', 'text':queries[query], 'positive':passages[positive],
                   'negatives':[passages[negative]], 'group_id':'query:'+query,
                   'query_id':query, 'positive_id':positive, 'negative_id':negative,
                   'training_approved':False, 'annotation_origin':'upstream MS MARCO, machine-translated mMARCO'}
            f.write(json.dumps(row, ensure_ascii=False) + '\n')
            emitted += 1
    manifest = {'status':'QUARANTINED: rights and benchmark overlap unresolved; not cleared for training',
                'sources':{id_: {k:r[k] for k in ('repository','revision','source_path','expected_sha256','expected_bytes')}
                           for id_,r in sources.items()},
                'output_file':output.name, 'output_sha256':digest(output), 'triples_requested':len(triples),
                'records':emitted, 'triples_missing_ids':missing, 'unique_query_ids':len(qids),
                'unique_passage_ids':len(pids), 'query_ids_resolved':len(queries), 'passage_ids_resolved':len(passages),
                'limitations':['first rows, not representative','translated source',
                               'no independent benchmark or source license review','not yet globally split/deduplicated']}
    atomic_json(out/'manifest.json', manifest)
    return manifest


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source-dir',type=Path,required=True)
    p.add_argument('--out-dir',type=Path,required=True)
    p.add_argument('--count',type=int,default=5000)
    args=p.parse_args()
    if not 1 <= args.count <= 50000:
        p.error('count must be 1..50000')
    print(json.dumps(join(args.source_dir,args.out_dir,args.count),ensure_ascii=False,indent=2))


if __name__ == '__main__':
    main()
