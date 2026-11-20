#!/usr/bin/env python3
"""Check exact text overlap with the isolated, verified MTEB RiaNews test set."""
import argparse
import hashlib
import json
from collections import defaultdict
from pathlib import Path
import pyarrow.parquet as pq
from prepare import clean, ocr_annotation_texts
from probe import atomic_json
from verify_downloads import digest


def key(text):
    return hashlib.sha256(clean(text).casefold().encode('utf-8')).hexdigest()


def check(candidate,source_dir):
    manifest=json.loads((candidate/'manifest.json').read_text())
    fingerprints=defaultdict(list)
    for item in manifest['files']:
        path=(candidate/item['path']).resolve()
        if path.parent!=candidate.resolve() or digest(path)!=item['sha256']:
            raise ValueError('candidate file hash or path mismatch')
        with path.open(encoding='utf-8') as f:
            for line in f:
                row=json.loads(line)
                fields = [('text',row['text']),('positive',row.get('positive','')),
                          ('ocr',row.get('ocr','')),('detailed_caption',row.get('detailed_caption','')),
                          ('subtitles',row.get('subtitles',''))]
                fields.extend(('ocr_annotation_'+kind, text) for kind,text in ocr_annotation_texts(row,include_words=True))
                for role,value in fields+[("negative",v) for v in row.get('negatives',[])]:
                    if clean(value):fingerprints[key(value)].append({'dataset_id':row['dataset_id'],'record_id':row['record_id'],'split':row['split'],'role':role})
    eval_manifest=json.loads((source_dir/'manifest-k.json').read_text())
    required={'mteb-ria-eval-queries','mteb-ria-eval-qrels'} | {f'mteb-ria-eval-corpus-{i:02d}' for i in range(4)}
    records=eval_manifest['records']
    if not required.issubset(records):raise ValueError('complete MTEB evaluation set not downloaded')
    results=[];matched=set();scanned=0
    for id_ in sorted(required):
        item=records[id_]
        if item.get('status') not in ('verified','verified_existing') or item.get('evaluation_only') is not True:
            raise ValueError(id_+': evaluation file not fully verified or not isolated')
        path=Path(item['local_path'])
        if path.stat().st_size!=item['expected_bytes'] or digest(path)!=item['expected_sha256']:
            raise ValueError(id_+': evaluation file SHA256 mismatch')
        if id_.endswith('qrels'):continue
        parquet=pq.ParquetFile(path)
        if 'text' not in parquet.schema_arrow.names:raise ValueError(id_+': text column absent')
        for batch in parquet.iter_batches(columns=['text'],batch_size=256,use_threads=False):
            for text in batch.column(0).to_pylist():
                if not isinstance(text,str):continue
                scanned+=1;sha=key(text)
                if sha in fingerprints:
                    matched.add(sha)
    for sha in sorted(matched):
        results.append({'text_sha256':sha,'candidate_occurrences':fingerprints[sha]})
    return {'benchmark':'mteb/RiaNewsRetrieval','evaluation_only':True,'scanned_text_rows':scanned,
            'matched_distinct_texts':len(matched),'matches':results,
            'candidate_manifest_sha256':digest(candidate/'manifest.json'),
            'limitations':['exact NFKC whitespace and casefold equality only',
                           'no near-duplicate, translation, title concatenation or other benchmark screening',
                           'other metadata beyond OCR text annotations are not independently scanned',
                           'matches are reported, not automatically removed']}


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--candidate',type=Path,required=True)
    p.add_argument('--source-dir',type=Path,required=True);p.add_argument('--out',type=Path,required=True)
    args=p.parse_args();result=check(args.candidate,args.source_dir);atomic_json(args.out,result)
    print(json.dumps({'scanned_text_rows':result['scanned_text_rows'],'matched_distinct_texts':result['matched_distinct_texts']},ensure_ascii=False))


if __name__=='__main__':main()
