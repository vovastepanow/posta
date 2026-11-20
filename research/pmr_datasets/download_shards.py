#!/usr/bin/env python3
"""Download pinned public source shards to HDD, quarantined until reviewed.

Downloads are explicit, resumable and validated against HF LFS SHA256. Reserves
space on the destination filesystem; no credentials or gated access accepted.
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import urllib.parse
from pathlib import Path
from datetime import datetime, timezone
from probe import atomic_json, get_json

# (dataset ID, repository, immutable revision, path).  Evaluation sources are
# marked separately and must never be used as training data.
PLAN = [
 ('kalm-retrieval','KaLM-Embedding/KaLM-embedding-finetuning-data','e9443ab6f5d4dc29c79cea03834e932428ed6ab1','AFQMC/train-00000-of-00001.parquet'),
 ('bvd-video','laion/BVD-V-55M-URLs','c67a81a860651c0672cc10c49e487099712f6ab5','clips/0000.parquet'),
 ('librispeech','openslr/librispeech_asr','71cacbfb7e2354c4226d01e70d77d5fca3d04ba1','clean/train.100/0000.parquet'),
 ('memexplain','QCRI/MemeXplain','2d08e071512ad6d6ad40bb18f1f793124ca25968','HatefulMemeXplain/train-00000-of-00007.parquet'),
 ('wit','wikimedia/wit_base','ff6d4fb32fd566d3a1fa20e946cba3234179465e','data/train-00000-of-00330.parquet'),
 ('fineweb2-hq-ru','epfml/FineWeb2-HQ','c0c06e94fd3a44ae9e802b2b0fc533817601eb5e','rus_Cyrl/000_00000.parquet'),
 ('fineweb-en','HuggingFaceFW/fineweb','9bb295ddab0e05d785b879661af7260fed5140fc','sample/10BT/000_00000.parquet'),
 ('videoufo','WenhaoWang/VideoUFO','683425832ad5b35d57d213963ecec18d5b689652','VideoUFO_tar/VideoUFO_1.tar'),
 ('videoufo-annotations','WenhaoWang/VideoUFO','683425832ad5b35d57d213963ecec18d5b689652','VideoUFO.csv'),
 ('fineweb2-ru','HuggingFaceFW/fineweb-2','af9c13333eb981300149d5ca60a8e9d659b276b9','data/rus_Cyrl/train/000_00000.parquet'),
 ('mmarco-ru','unicamp-dl/mmarco','6d039c4638c0ba3e46a9cb7b498b145e7edc6230','data/google/collections/russian_collection.tsv'),
 ('mmarco-ru-queries','unicamp-dl/mmarco','6d039c4638c0ba3e46a9cb7b498b145e7edc6230','data/google/queries/train/russian_queries.train.tsv'),
 ('mmarco-train-ids','unicamp-dl/mmarco','6d039c4638c0ba3e46a9cb7b498b145e7edc6230','data/triples.train.ids.small.tsv'),
 ('nvidia-ocr-ru','nvidia/OCR-Synthetic-Multilingual-v1','69696a1cc543ef3a0f8e9892a89c17293e915263','ru/train/train_000.h5'),
 ('nvidia-ocr-en','nvidia/OCR-Synthetic-Multilingual-v1','69696a1cc543ef3a0f8e9892a89c17293e915263','en/train/train_000.h5'),
 ('mmeb-v3-eval','VLM2Vec/MMEB-V3','4a5560b2b64384204b6fea8a82ea986eba51f5aa','omniset.tar.gz'),
 ('bge-m3','Shitao/bge-m3-data','a69db8b86e9c1767d193ee0de95e5c4001a71eae','bge-m3-data.tar.gz'),
 ('datacomp-hq','laion/datacomp-hq','0ecce74742607de756fac46d6c9100a3c8db2ce7','datacomp-hq-V0_1_1.parquet'),
]

# Fetch all remaining original training shards of MemeXplain. Its dev/test and
# separately annotated benchmark files are intentionally not part of this list.
PLAN += [
    (f'memexplain-{index:02d}', 'QCRI/MemeXplain',
     '2d08e071512ad6d6ad40bb18f1f793124ca25968',
     f'HatefulMemeXplain/train-{index:05d}-of-00007.parquet')
    for index in range(1, 7)
]



def sha256_file(path):
    h = hashlib.sha256()
    with path.open('rb') as f:
        for block in iter(lambda: f.read(1024 * 1024), b''):
            h.update(block)
    return h.hexdigest()


def metadata(repo, revision, path):
    parent, _, _ = path.rpartition('/')
    url = 'https://huggingface.co/api/datasets/' + urllib.parse.quote(repo, safe='/') + '/tree/' + revision
    if parent:
        url += '/' + urllib.parse.quote(parent, safe='/')
    entries = get_json(url + '?limit=100')
    item = next((x for x in entries if x.get('path') == path and x.get('type') == 'file'), None)
    digest = (item.get('lfs') or {}).get('oid') if item else None
    if not isinstance(digest, str) or not re.fullmatch(r'[0-9a-f]{64}', digest):
        raise ValueError('file or valid upstream LFS SHA256 not found on bounded tree page')
    return int(item['size']), digest


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--out-dir', type=Path, required=True)
    p.add_argument('--reserve-gib', type=int, default=40)
    p.add_argument('--manifest-name', default='download-manifest.json')
    p.add_argument('--ids', nargs='+', choices=[item[0] for item in PLAN])
    args = p.parse_args()
    if args.reserve_gib < 10:
        p.error('reserve must be at least 10 GiB')
    if Path(args.manifest_name).name != args.manifest_name:
        p.error('manifest-name must be a plain filename')
    args.out_dir.mkdir(parents=True, exist_ok=True)
    manifest_path = args.out_dir / args.manifest_name
    if manifest_path.exists():
        manifest = json.loads(manifest_path.read_text())
    else:
        manifest = {'status': 'quarantined; source review and benchmark screening required',
                    'records': {}, 'blocked': {'megapairs': 'gated; terms not accepted',
                    'common-voice': 'audited HF mirror has no audio'},
                    'note': 'One listed shard per source, not complete datasets; mmeb-v3 is evaluation only.'}
    for id_, repo, rev, remote_path in PLAN:
        if args.ids and id_ not in args.ids:
            continue
        record = {'dataset_id': id_, 'repository': repo, 'revision': rev, 'source_path': remote_path,
                  'training_approved': False, 'evaluation_only': id_ == 'mmeb-v3-eval'}
        manifest['records'][id_] = record
        try:
            size, expected = metadata(repo, rev, remote_path)
            record.update(expected_bytes=size, expected_sha256=expected)
            final = args.out_dir / id_ / Path(remote_path).name
            final.parent.mkdir(parents=True, exist_ok=True)
            part = final.with_name(final.name + '.part')
            record['local_path'] = str(final)
            if final.exists():
                if final.stat().st_size != size or sha256_file(final) != expected:
                    raise ValueError('existing file failed size or hash; not overwritten')
                record['status'] = 'verified_existing'
            else:
                partial = part.stat().st_size if part.exists() else 0
                if partial > size:
                    raise ValueError('partial file exceeds expected size; not overwritten')
                required = size - partial + args.reserve_gib * (1024 ** 3)
                if shutil.disk_usage(args.out_dir).free < required:
                    raise ValueError('insufficient free space while maintaining reserve')
                record.update(status='downloading', started_utc=datetime.now(timezone.utc).isoformat())
                atomic_json(manifest_path, manifest)
                url = ('https://huggingface.co/datasets/' + urllib.parse.quote(repo, safe='/') +
                       '/resolve/' + rev + '/' + urllib.parse.quote(remote_path, safe='/') + '?download=true')
                print('downloading', id_, size, flush=True)
                cmd = ['curl','--fail','--location','--silent','--show-error','--retry','2',
                       '--connect-timeout','25','--speed-time','120','--speed-limit','1024',
                       '--max-filesize', str(size), '--continue-at','-', '--output',str(part),url]
                subprocess.run(cmd, check=True)
                if part.stat().st_size != size or sha256_file(part) != expected:
                    raise ValueError('download size or SHA256 mismatch; partial retained')
                os.replace(part, final)
                record['status'] = 'verified'
            record['completed_utc'] = datetime.now(timezone.utc).isoformat()
        except (OSError, ValueError, subprocess.SubprocessError) as exc:
            record.update(status='error', error=str(exc)[:300])
        atomic_json(manifest_path, manifest)
        print(id_, record['status'], record.get('error', ''), flush=True)
    return int(any(x['status'] == 'error' for x in manifest['records'].values()))


if __name__ == '__main__':
    sys.exit(main())
