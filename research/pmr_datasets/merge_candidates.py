#!/usr/bin/env python3
"""Merge local quarantined source candidates and recompute one global split."""
import argparse
import json
import math
import shutil
from collections import Counter
from pathlib import Path
from prepare import clean, normalize, ocr_annotation_texts, sha, write_jsonl
from prepare_local_shards import ensure_speech_holdouts, file_hash, split_without_negative_leakage
from prepare_videoufo import parse_timestamp
from probe import CATALOG, atomic_json


def merge(inputs, output, exclude_report=None, replacements=None):
    if not inputs:raise ValueError('at least one input directory is required')
    replacements = {key: value.resolve() for key, value in (replacements or {}).items()}
    if any(path not in {root.resolve() for root in inputs} for path in replacements.values()):
        raise ValueError('replacement directory must be one of the inputs')
    replacement_seen, replacement_skipped = set(), Counter()
    blocked_hashes = set()
    if exclude_report is not None:
        if len(inputs) != 1:
            raise ValueError('benchmark report must be applied to exactly one scanned candidate')
        report = json.loads(exclude_report.read_text(encoding='utf-8'))
        if report.get('benchmark') != 'mteb/RiaNewsRetrieval' or report.get('candidate_manifest_sha256') != file_hash(inputs[0]/'manifest.json'):
            raise ValueError('benchmark report does not match input candidate')
        blocked_hashes = {entry['text_sha256'] for entry in report['matches']}
    catalog = json.loads(CATALOG.read_text(encoding='utf-8'))
    evaluation_repos = {x['repo'] for x in catalog['datasets'] if x['use'] == 'independent_evaluation'}
    output.mkdir(parents=True,exist_ok=False);(output/'media').mkdir()
    rows, extras, source_map, input_refs, copied = [], [], {}, [], set()
    exclusions, rejections, benchmark_negatives_removed = [], [], 0
    for root in inputs:
        manifest_path=root/'manifest.json';manifest=json.loads(manifest_path.read_text(encoding='utf-8'))
        input_refs.append({'directory':str(root.resolve()),'manifest_sha256':file_hash(manifest_path)})
        sources=manifest['sources']
        for id_,source in sources.items():
            source_keys = ('repository', 'revision', 'source_path', 'expected_sha256', 'expected_bytes')
            if id_ in source_map and any(source_map[id_].get(k) != source.get(k) for k in source_keys):
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
                    replacement = replacements.get(raw.get('dataset_id'))
                    if replacement is not None and root.resolve() != replacement:
                        replacement_skipped[raw['dataset_id']] += 1
                        continue
                    source=sources.get(raw['dataset_id'])
                    if not source:raise ValueError('missing row source provenance')
                    if source.get('repository') in evaluation_repos:
                        raise ValueError('independent evaluation source cannot enter candidate data')
                    try:
                        row=normalize(raw)
                    except ValueError as exc:
                        rejections.append({'dataset_id':raw.get('dataset_id'), 'record_id':raw.get('record_id'),
                                           'reason':str(exc)[:200]})
                        continue
                    anchors = [row['text'], row['positive'], raw.get('ocr',''), raw.get('detailed_caption',''), raw.get('subtitles','')]
                    anchors.extend(text for _,text in ocr_annotation_texts(raw,include_words=True))
                    if any(sha(clean(value).casefold()) in blocked_hashes for value in anchors if clean(value)):
                        exclusions.append({'dataset_id':row['dataset_id'],'record_id':row['record_id'],'reason':'exact benchmark text'})
                        continue
                    retained = [value for value in row['negatives'] if sha(clean(value).casefold()) not in blocked_hashes]
                    benchmark_negatives_removed += len(row['negatives']) - len(retained)
                    row['negatives'] = retained
                    row['_fingerprint'] = normalize(row)['_fingerprint']
                    meta={k:v for k,v in raw.items() if k not in row and not k.startswith('_')}
                    if not clean(meta.get('annotation_origin')):
                        meta['annotation_origin'] = {
                            'text':'upstream source document',
                            'speech_text':'upstream speech transcription',
                            'image_text':'upstream image caption',
                            'retrieval':'upstream relevance annotations',
                            'ocr_text':'upstream OCR labels',
                            'meme_text':'upstream meme annotation',
                            'video_text':'upstream video caption',
                        }[row['kind']]
                    if row['kind'] == 'video_text':
                        try:
                            start = parse_timestamp(meta.get('source_start_time'))
                            end = parse_timestamp(meta.get('source_end_time'))
                            duration = float(meta.get('duration_seconds') or 0)
                            if end <= start or not math.isfinite(duration) or duration <= 0:
                                raise ValueError('invalid video duration or timestamps')
                            meta.update(source_start_seconds=start, source_end_seconds=end,
                                        source_vs_clip_duration_delta_seconds=duration-(end-start))
                        except (ValueError, TypeError) as exc:
                            rejections.append({'dataset_id':row['dataset_id'],'record_id':row['record_id'],
                                               'reason':str(exc)[:200]})
                            continue
                    if clean(meta.get('source_url')):
                        row['_source_url'] = clean(meta['source_url'])
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
                    if replacement is not None:
                        replacement_seen.add(raw['dataset_id'])
    if replacements.keys() - replacement_seen:
        raise ValueError('replacement source has no records: ' + ', '.join(sorted(replacements.keys() - replacement_seen)))
    assignments,dropped=split_without_negative_leakage(rows)
    speech_holdouts=ensure_speech_holdouts(rows,assignments)
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
    exclusions_path=output/'benchmark-exclusions.jsonl';write_jsonl(exclusions_path,exclusions)
    rejections_path=output/'rejections.jsonl';write_jsonl(rejections_path,rejections)
    license_by_repo = {entry['repo']: entry for entry in catalog['datasets']}
    for source in source_map.values():
        license_record = license_by_repo.get(source.get('repository'), {})
        source['license_claim'] = license_record.get('license_claim', 'unverified')
        source['license_review_note'] = license_record.get('license_review', 'source review required')
        source['license_review_status'] = 'pending'
    manifest={'version':'posta-dataset-v0.1-merged-candidate',
              'status':'QUARANTINED: source rights, privacy and benchmark screening required',
              'inputs':input_refs,'sources':source_map,'files':files,
              'counts':[{'kind':k,'split':s,'language':l,'rows':n} for (k,s,l),n in sorted(counts.items())],
              'accepted':sum(counts.values()),'exact_duplicates_removed':len(duplicates),
              'superseded_source_rows':dict(replacement_skipped),
              'duplicates_report':{'path':duplicates_path.name,'sha256':file_hash(duplicates_path)},
              'rejections_report':{'rows':len(rejections),'path':rejections_path.name,'sha256':file_hash(rejections_path)},
              'negatives_removed_to_prevent_cross_split_overlap':dropped,'speaker_holdouts_created':speech_holdouts,
              'benchmark_exclusions':{'rows':len(exclusions),'negatives':benchmark_negatives_removed,
                                      'path':exclusions_path.name,'sha256':file_hash(exclusions_path),
                                      'report_sha256':file_hash(exclude_report) if exclude_report else None},
              'limitations':['all rows require rights and privacy review',
                             'near-duplicate, translated and independent benchmark overlap not screened',
                             'local validation/test are not independent benchmarks',
                             'split assignments can change when data components change']}
    atomic_json(output/'manifest.json',manifest);return manifest


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--input',type=Path,action='append',required=True)
    p.add_argument('--out-dir',type=Path,required=True)
    p.add_argument('--exclude-report',type=Path,help='exact benchmark overlap report for the single input')
    p.add_argument('--replace-source',action='append',default=[],metavar='DATASET_ID=INPUT_DIR')
    args=p.parse_args()
    replacements={}
    for value in args.replace_source:
        id_, separator, directory=value.partition('=')
        if not separator or not id_ or not directory or id_ in replacements:
            p.error('replace-source must be a unique DATASET_ID=INPUT_DIR')
        replacements[id_]=Path(directory)
    result=merge(args.input,args.out_dir,args.exclude_report,replacements)
    print(json.dumps({'accepted':result['accepted'],'duplicates':result['exact_duplicates_removed'],
                      'negatives_removed':result['negatives_removed_to_prevent_cross_split_overlap'],
                      'counts':result['counts']},ensure_ascii=False,indent=2))


if __name__=='__main__':main()
