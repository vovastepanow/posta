#!/usr/bin/env python3
"""Read a tiny, unverified/quarantined OCR preview via bounded HTTP ranges."""
import argparse
import hashlib
import io
import json
from pathlib import Path
import h5py
from PIL import Image
from inspect_remote_hdf5 import RangeReader
from probe import atomic_json

URL='https://huggingface.co/datasets/nvidia/OCR-Synthetic-Multilingual-v1/resolve/69696a1cc543ef3a0f8e9892a89c17293e915263/ru/train/train_000.h5?download=true'
SIZE=10413868372
REVISION='69696a1cc543ef3a0f8e9892a89c17293e915263'
EXPECTED='d96e70a314fb56b6b938ec141f88e8e1ea69b8133a5820e4d81d32584853eff5'


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('--out-dir',type=Path,required=True)
    p.add_argument('--count',type=int,default=10);p.add_argument('--max-mib',type=int,default=32)
    args=p.parse_args()
    if not 1<=args.count<=50 or not 1<=args.max_mib<=64:p.error('count 1..50; max-mib 1..64')
    args.out_dir.mkdir(parents=True,exist_ok=False);(args.out_dir/'media').mkdir()
    reader=RangeReader(URL,SIZE,args.max_mib*1024*1024);rows=[];error=None
    try:
        with h5py.File(reader,'r') as f:
            for i in range(args.count):
                raw=f['images'][i].tobytes();label=f['labels'][i].decode('utf-8')
                ann=json.loads(f['annotations'][i].decode('utf-8'))
                with Image.open(io.BytesIO(raw)) as image:
                    if image.width*image.height>40_000_000:raise ValueError('image exceeds pixel limit')
                    image.load();fmt=image.format
                if fmt!='JPEG':raise ValueError('expected JPEG image')
                sha=hashlib.sha256(raw).hexdigest();name=sha+'.jpg'
                (args.out_dir/'media'/name).write_bytes(raw)
                rows.append({'dataset_id':'nvidia-ocr-ru','revision':REVISION,'source_row_index':i,
                             'source_sha256_expected_but_not_verified':EXPECTED,'kind':'ocr_text','language':'ru',
                             'text':label,'ocr_annotations':ann,'synthetic':True,'training_approved':False,
                             'media':{'path':'media/'+name,'sha256':sha,'size_bytes':len(raw)}})
    except (OSError,ValueError,KeyError,UnicodeError) as exc:error=str(exc)[:250]
    output=args.out_dir/'preview.jsonl'
    with output.open('w',encoding='utf-8') as f:
        for row in rows:f.write(json.dumps(row,ensure_ascii=False)+'\n')
    manifest={'status':'QUARANTINED; remote ranges only, full source SHA256 not verified',
              'source_revision':REVISION,'expected_source_sha256':EXPECTED,'source_bytes':SIZE,
              'range_bytes_read':reader.downloaded,'range_limit_bytes':args.max_mib*1024*1024,
              'records':len(rows),'preview_sha256':hashlib.sha256(output.read_bytes()).hexdigest(),
              'error':error}
    atomic_json(args.out_dir/'manifest.json',manifest);print(json.dumps(manifest,ensure_ascii=False,indent=2))


if __name__=='__main__':main()
