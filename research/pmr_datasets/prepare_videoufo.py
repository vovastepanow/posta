#!/usr/bin/env python3
"""Extract a bounded, quarantined VideoUFO sample from verified local files."""
import argparse
import csv
import hashlib
import json
import subprocess
import tarfile
from collections import Counter
from pathlib import Path
from prepare import normalize, partition, write_jsonl
from probe import atomic_json


def digest(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for b in iter(lambda:f.read(1024*1024),b''):
            h.update(b)
    return h.hexdigest()


def sources(root):
    ids=('videoufo','videoufo-annotations')
    found={}
    for manifest in root.glob('manifest-*.json'):
        for id_,row in json.loads(manifest.read_text())['records'].items():
            if id_ in ids and row.get('status') in ('verified','verified_existing'):
                path=Path(row['local_path'])
                if path.stat().st_size != row['expected_bytes'] or digest(path) != row['expected_sha256']:
                    raise ValueError(id_+': size or SHA256 mismatch')
                found[id_]=row
    if set(found)!=set(ids):
        raise ValueError('video archive and annotation CSV must be fully downloaded and verified')
    if len({row['revision'] for row in found.values()}) != 1:
        raise ValueError('archive and annotation revision mismatch')
    return found


def build(root,out,count):
    records=sources(root)
    archive_path=Path(records['videoufo']['local_path'])
    csv_path=Path(records['videoufo-annotations']['local_path'])
    out.mkdir(parents=True,exist_ok=False)
    media=out/'media';media.mkdir()
    with tarfile.open(archive_path,'r:') as archive:
        candidates={}
        for member in archive:
            name=Path(member.name).name
            if member.isfile() and name.endswith('.mp4') and 0 < member.size <= 32*1024*1024:
                candidates[name[:-4]]=member
                if len(candidates)>=count*5:
                    break
        annotations={}
        with csv_path.open(encoding='utf-8',newline='') as f:
            for row in csv.DictReader(f):
                id_=row.get('ID')
                if id_ in candidates and row.get('Brief Caption'):
                    annotations[id_]=row
                    if len(annotations)>=count*2:
                        break
        rows, provenance, rejected=[],[],Counter()
        for id_ in sorted(annotations):
            try:
                member=candidates[id_]
                stream=archive.extractfile(member)
                if stream is None: raise ValueError('missing archive content')
                data=stream.read(32*1024*1024+1)
                if len(data)!=member.size: raise ValueError('video size mismatch')
                sha=hashlib.sha256(data).hexdigest();path=media/(sha+'.mp4')
                path.write_bytes(data)
                proc=subprocess.run(['ffprobe','-v','error','-show_entries','format=duration:stream=codec_name,codec_type',
                                     '-of','json',str(path)],capture_output=True,timeout=30)
                if proc.returncode: raise ValueError('ffprobe failed')
                meta=json.loads(proc.stdout)
                if not any(s.get('codec_type')=='video' for s in meta.get('streams',[])):
                    raise ValueError('no video stream')
                decoded=subprocess.run(['ffmpeg','-v','error','-i',str(path),'-map','0:v:0','-f','null','-'],
                                       capture_output=True,timeout=60)
                if decoded.returncode: raise ValueError('video decode failed')
                annotation=annotations[id_]
                row=normalize({'dataset_id':'videoufo','record_id':id_,'kind':'video_text','language':'en',
                               'text':annotation['Brief Caption'],'media_sha256':sha,'group_id':id_.rsplit('.',1)[0]})
                rows.append(row)
                provenance.append({'source_revision':records['videoufo']['revision'],
                                   'archive_sha256':records['videoufo']['expected_sha256'],
                                   'annotation_sha256':records['videoufo-annotations']['expected_sha256'],
                                   'archive_member':member.name,'media':{'path':'media/'+path.name,'sha256':sha,'size_bytes':len(data)},
                                   'source_start_time':annotation.get('Start Time'),'source_end_time':annotation.get('End Time'),
                                   'detailed_caption':annotation.get('Detailed Caption'),'duration_seconds':(meta.get('format') or {}).get('duration'),
                                   'annotation_origin':'upstream VideoUFO; rights and benchmark overlap unverified','training_approved':False})
                if len(rows)>=count: break
            except (OSError,ValueError,subprocess.SubprocessError) as e:
                rejected[str(e)[:120]]+=1
    assignments=partition(rows);groups={};seen=set();duplicates=0
    for i,row in enumerate(rows):
        if row['_fingerprint'] in seen:
            duplicates+=1;continue
        seen.add(row['_fingerprint'])
        result={k:v for k,v in row.items() if not k.startswith('_')}
        result.update(provenance[i],split=assignments[i])
        groups.setdefault(assignments[i],[]).append(result)
    files=[]
    for split,batch in sorted(groups.items()):
        path=out/('video_text-'+split+'.jsonl');write_jsonl(path,batch)
        files.append({'path':path.name,'rows':len(batch),'sha256':digest(path)})
    manifest={'status':'QUARANTINED: review only; not globally merged or benchmark screened',
              'sources':{k:{x:v[x] for x in ('repository','revision','source_path','expected_sha256','expected_bytes')} for k,v in records.items()},
              'archive_candidates':len(candidates),'matched_annotations':len(annotations),
              'accepted':sum(x['rows'] for x in files),'exact_duplicates_removed':duplicates,
              'files':files,'rejections':dict(rejected),
              'limitations':['source video rights need review','not cross-checked against other data or benchmarks',
                             'source timestamps refer to original video; clips may begin at zero',
                             'first archive entries are not representative']}
    atomic_json(out/'manifest.json',manifest)
    return manifest


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source-dir',type=Path,required=True);p.add_argument('--out-dir',type=Path,required=True)
    p.add_argument('--count',type=int,default=100);args=p.parse_args()
    if not 1 <= args.count <= 1000:p.error('count must be 1..1000')
    print(json.dumps(build(args.source_dir,args.out_dir,args.count),ensure_ascii=False,indent=2))


if __name__=='__main__':main()
