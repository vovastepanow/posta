#!/usr/bin/env python3
"""Extract bounded synthetic OCR examples from complete verified HDF5 shards."""
import argparse
import hashlib
import io
import json
from collections import Counter
from pathlib import Path
import h5py
from PIL import Image
from prepare import normalize, partition, write_jsonl
from probe import atomic_json


def digest(path):
    h=hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda:f.read(1024*1024),b''):h.update(block)
    return h.hexdigest()


def sources(root):
    found={}
    for m in root.glob('manifest-*.json'):
        for id_,row in json.loads(m.read_text())['records'].items():
            if id_ in ('nvidia-ocr-ru','nvidia-ocr-en') and row.get('status') in ('verified','verified_existing'):
                path=Path(row['local_path'])
                if path.stat().st_size!=row['expected_bytes'] or digest(path)!=row['expected_sha256']:
                    raise ValueError(id_+': size or SHA256 mismatch')
                found[id_]=row
    if not found:raise ValueError('no fully downloaded and verified OCR shards')
    return found


def as_text(value):
    return value.decode('utf-8') if isinstance(value,bytes) else str(value)


def build(root,out,count):
    verified=sources(root);out.mkdir(parents=True,exist_ok=False);(out/'media').mkdir()
    rows,provenance,rejected=[],[],Counter()
    for id_,source in sorted(verified.items()):
        language='ru' if id_.endswith('-ru') else 'en'
        with h5py.File(source['local_path'],'r') as f:
            required=('images','labels','annotations','dimensions','qualities','sample_ids')
            if not all(x in f for x in required):raise ValueError(id_+': unexpected HDF5 schema')
            if len({len(f[x]) for x in required})!=1:raise ValueError(id_+': HDF5 column lengths differ')
            for i in range(min(count,len(f['labels']))):
                try:
                    label=as_text(f['labels'][i]);raw=f['images'][i].tobytes()
                    if not raw or len(raw)>16*1024*1024:raise ValueError('invalid image bytes')
                    with Image.open(io.BytesIO(raw)) as im:
                        if im.width*im.height>40_000_000:raise ValueError('image exceeds pixel limit')
                        im.load();fmt=im.format;dims=[im.width,im.height]
                    ext={'JPEG':'.jpg','PNG':'.png','WEBP':'.webp'}.get(fmt)
                    if not ext:raise ValueError('unsupported image format')
                    annotations=json.loads(as_text(f['annotations'][i]))
                    sha=hashlib.sha256(raw).hexdigest();file=out/'media'/(sha+ext)
                    file.write_bytes(raw)
                    sample_id=str(f['sample_ids'][i])
                    row=normalize({'dataset_id':id_,'record_id':sample_id,'kind':'ocr_text','language':language,
                                   'text':label,'media_sha256':sha,'group_id':'sample:'+sample_id})
                    rows.append(row)
                    provenance.append({'source_revision':source['revision'],'source_file':source['source_path'],
                                       'source_file_sha256':source['expected_sha256'],'source_row_index':i,
                                       'annotation_origin':'NVIDIA synthetic OCR', 'synthetic':True,
                                       'ocr':label,'ocr_annotations':annotations,'dimensions':dims,
                                       'source_dimensions':[int(x) for x in f['dimensions'][i]],
                                       'quality':int(f['qualities'][i]),
                                       'media':{'path':'media/'+file.name,'sha256':sha,'size_bytes':len(raw)},
                                       'training_approved':False})
                except (OSError,ValueError,UnicodeError,KeyError,TypeError) as exc:
                    rejected[f'{id_}: {str(exc)[:120]}']+=1
    assignments=partition(rows);grouped={};seen=set();duplicates=0
    for i,row in enumerate(rows):
        if row['_fingerprint'] in seen:duplicates+=1;continue
        seen.add(row['_fingerprint']);result={k:v for k,v in row.items() if not k.startswith('_')}
        result.update(provenance[i],split=assignments[i]);grouped.setdefault(assignments[i],[]).append(result)
    files=[];counts=Counter()
    for split,batch in sorted(grouped.items()):
        path=out/('ocr_text-'+split+'.jsonl');write_jsonl(path,batch)
        files.append({'path':path.name,'rows':len(batch),'sha256':digest(path)})
        counts.update((split,x['language']) for x in batch)
    manifest={'status':'QUARANTINED: source review and global benchmark screening required',
              'sources':{id_:{k:v[k] for k in ('repository','revision','source_path','expected_sha256','expected_bytes')}
                         for id_,v in verified.items()},
              'accepted':sum(x['rows'] for x in files),'exact_duplicates_removed':duplicates,'files':files,
              'counts':[{'split':s,'language':l,'rows':n} for (s,l),n in sorted(counts.items())],
              'rejections':dict(rejected),'limitations':['synthetic text and images','not globally merged or screened against other datasets',
                                                          'dataset license and source use still require review']}
    atomic_json(out/'manifest.json',manifest);return manifest


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--source-dir',type=Path,required=True)
    p.add_argument('--out-dir',type=Path,required=True);p.add_argument('--count',type=int,default=1000)
    args=p.parse_args()
    if not 1<=args.count<=5000:p.error('count must be 1..5000 per source')
    print(json.dumps(build(args.source_dir,args.out_dir,args.count),ensure_ascii=False,indent=2))


if __name__=='__main__':main()
