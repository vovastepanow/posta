#!/usr/bin/env python3
"""Merge local quarantined source candidates and recompute one global split."""
import argparse
import json
import shutil
from collections import Counter
from pathlib import Path
from prepare import normalize, write_jsonl
from prepare_local_shards import file_hash, split_without_negative_leakage
from probe import atomic_json


def merge(inputs, output):
    if not inputs:raise ValueError('at least one input directory is required')
    output.mkdir(parents=True,exist_ok=False);(output/'media').mkdir()
    rows, extras, source_map, input_refs, copied = [], [], {}, [], set()
    for root in inputs:
        manifest_path=root/'manifest.json';manifest=json.loads(manifest_path.read_text(encoding='utf-8'))
        input_refs.append({'directory':str(root.resolve()),'manifest_sha256':file_hash(manifest_path)})
        sources=manifest['sources']
        for id_,source in sources.items():
            if id_ in source_map and source_map[id_] != source:
                raise ValueError('conflicting source provenance: '+id_)
            source_map[id_]=source
        files=manifest.get('files')
        if files is None:
            files=[{'path':manifest['output_file'],'sha256':manifest['output_sha256']}]
        for entry in files:
            path=(root/entry['path']).resolve()
            if path.parent != root.resolve() or file_hash(path)!=entry['sha256']:
                raise ValueError('input JSONL path or SHA256 mismatch')
            with path.open(encoding='utf-8') as f:
                for line in f:
                    raw=json.loads(line)
                    if raw.get('training_approved') is not False:raise ValueError('input is not quarantined')
                    source=sources.get(raw['dataset_id'])
                    if not source:raise ValueError('missing row source provenance')
                    row=normalize(raw)
                    meta={k:v for k,v in raw.items() if k not in row and not k.startswith('_')}
                    meta.update(dataset_revision=source['revision'],source_file=source['source_path'],
                                source_file_sha256=source['expected_sha256'],training_approved=False)
                    if row['media_sha256']:
                        original=(root/raw['media']['path']).resolve()
                        if original.parent != (root/'media').resolve() or file_hash(original)!=row['media_sha256']:
                            raise ValueError('input media path or SHA256 mismatch')
                        suffix=original.suffix.lower()
                        if suffix not in ('.jpg','.png','.webp','.flac','.mp4'):
                            raise ValueError('unsupported media suffix')
                        target=output/'media'/(row['media_sha256']+suffix)
                        if row['media_sha256'] not in copied:
                            shutil.copyfile(original,target)
                            if file_hash(target)!=row['media_sha256']:raise ValueError('copied media SHA256 mismatch')
                            copied.add(row['media_sha256'])
                        meta['media']=dict(raw['media'],path='media/'+target.name)
                    rows.append(row);extras.append(meta)
    assignments,dropped=split_without_negative_leakage(rows)
    seen,grouped,duplicates={}, {}, []
    for i,row in enumerate(rows):
        key=row['_fingerprint']
        if key in seen:
            duplicates.append({'dataset_id':row['dataset_id'],'record_id':row['record_id'],
                               'kept_dataset_id':seen[key][0],'kept_record_id':seen[key][1]})
            continue
        seen[key]=(row['dataset_id'],row['record_id'])
        record={k:v for k,v in row.items() if not k.startswith('_')}
        record.update(extras[i],split=assignments[i])
        grouped.setdefault((row['kind'],assignments[i]),[]).append(record)
    files=[];counts=Counter()
    for (kind,split),batch in sorted(grouped.items()):
        path=output/f'{kind}-{split}.jsonl';write_jsonl(path,batch)
        files.append({'path':path.name,'sha256':file_hash(path),'rows':len(batch)})
        counts.update((kind,split,r['language']) for r in batch)
    duplicates_path=output/'duplicates.jsonl';write_jsonl(duplicates_path,duplicates)
    manifest={'version':'posta-dataset-v0.1-merged-candidate',
              'status':'QUARANTINED: source rights, privacy and benchmark screening required',
              'inputs':input_refs,'sources':source_map,'files':files,
              'counts':[{'kind':k,'split':s,'language':l,'rows':n} for (k,s,l),n in sorted(counts.items())],
              'accepted':sum(counts.values()),'exact_duplicates_removed':len(duplicates),
              'duplicates_report':{'path':duplicates_path.name,'sha256':file_hash(duplicates_path)},
              'negatives_removed_to_prevent_cross_split_overlap':dropped,
              'limitations':['all rows require rights and privacy review',
                             'near-duplicate, translated and independent benchmark overlap not screened',
                             'local validation/test are not independent benchmarks',
                             'split assignments can change when data components change']}
    atomic_json(output/'manifest.json',manifest);return manifest


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',type=Path,action='append',required=True)
    p.add_argument('--out-dir',type=Path,required=True);args=p.parse_args()
    result=merge(args.input,args.out_dir)
    print(json.dumps({'accepted':result['accepted'],'duplicates':result['exact_duplicates_removed'],
                      'negatives_removed':result['negatives_removed_to_prevent_cross_split_overlap'],
                      'counts':result['counts']},ensure_ascii=False,indent=2))


if __name__=='__main__':main()
