#!/usr/bin/env python3
"""Aggregate task, language and split statistics without exporting example text."""
import argparse
import json
from collections import Counter,defaultdict
from pathlib import Path
from probe import atomic_json


def collect(root):
    manifest=json.loads((root/'manifest.json').read_text(encoding='utf-8'))
    grouped=defaultdict(Counter);speakers=defaultdict(set);total=0
    for item in manifest['files']:
        with (root/item['path']).open(encoding='utf-8') as f:
            for line in f:
                r=json.loads(line);key=(r['kind'],r['language'],r['split']);stats=grouped[key]
                text=r.get('text',''); stats['records']+=1;stats['characters']+=len(text)
                stats['whitespace_words']+=len(text.split());stats['negative_items']+=len(r.get('negatives',[]))
                if r['kind']=='retrieval' and not r.get('negatives'):stats['retrieval_without_negatives']+=1
                if r.get('media'):stats['media_bytes']+=r['media'].get('size_bytes',0)
                if r.get('speaker_id') is not None:speakers[r['split']].add(str(r['speaker_id']))
                total+=1
    return {'records':total,'groups':[{'kind':k,'language':l,'split':s,**dict(v)}
                                     for (k,l,s),v in sorted(grouped.items())],
            'distinct_speakers_by_split':{k:len(v) for k,v in sorted(speakers.items())},
            'notes':['Whitespace word counts are not model token counts.',
                     'Statistics do not establish legal clearance or independent benchmark isolation.']}


def main():
    p=argparse.ArgumentParser(description=__doc__);p.add_argument('directory',type=Path)
    p.add_argument('--out',type=Path,required=True);args=p.parse_args();result=collect(args.directory)
    atomic_json(args.out,result);print(json.dumps(result,ensure_ascii=False,indent=2))


if __name__=='__main__':main()
