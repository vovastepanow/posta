#!/usr/bin/env python3
"""Bounded HTTP Range HDF5 metadata inspection; no full-shard download."""
import argparse
import io
import itertools
import json
import urllib.request
from collections import OrderedDict
import h5py


class RangeReader(io.RawIOBase):
    def __init__(self,url,size,limit,block=262144):
        self.url,self.size,self.limit,self.block=url,size,limit,block
        self.pos=0;self.downloaded=0;self.cache=OrderedDict()
    def readable(self):return True
    def seekable(self):return True
    def tell(self):return self.pos
    def seek(self,offset,whence=0):
        pos=offset if whence==0 else self.pos+offset if whence==1 else self.size+offset
        if pos<0:raise ValueError('negative seek')
        self.pos=pos;return pos
    def readinto(self,target):
        if self.pos>=self.size:return 0
        total=min(len(target),self.size-self.pos);done=0
        while done<total:
            index=self.pos//self.block
            if index not in self.cache:
                start=index*self.block;end=min(start+self.block,self.size)-1
                needed=end-start+1
                if self.downloaded+needed>self.limit:raise ValueError('HTTP byte limit exceeded')
                req=urllib.request.Request(self.url,headers={'Range':f'bytes={start}-{end}',
                                           'User-Agent':'Posta-PMR-range-audit/0.1'})
                with urllib.request.urlopen(req,timeout=30) as response:
                    expected=f'bytes {start}-{end}/{self.size}'
                    if response.status!=206 or response.headers.get('Content-Range')!=expected:
                        raise ValueError('server did not honor exact HTTP Range')
                    data=response.read(needed+1)
                if len(data)!=needed:raise ValueError('range size mismatch')
                self.downloaded+=len(data);self.cache[index]=data
                if len(self.cache)>32:self.cache.popitem(last=False)
            data=self.cache[index];offset=self.pos%self.block
            n=min(total-done,len(data)-offset)
            target[done:done+n]=data[offset:offset+n]
            done+=n;self.pos+=n
        return done


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--url',required=True);p.add_argument('--size',type=int,required=True)
    p.add_argument('--max-mib',type=int,default=16);p.add_argument('--out',required=True)
    args=p.parse_args()
    if not 1<=args.max_mib<=64:p.error('max-mib must be 1..64')
    source=RangeReader(args.url,args.size,args.max_mib*1024*1024)
    report={'status':'incomplete','root_entries':[]}
    try:
        with h5py.File(source,'r') as f:
            for key in list(itertools.islice(f.keys(),10)):
                obj=f[key];entry={'name':key,'type':type(obj).__name__}
                if isinstance(obj,h5py.Dataset):entry.update(shape=list(obj.shape),dtype=str(obj.dtype))
                if isinstance(obj,h5py.Group):entry['first_children']=list(itertools.islice(obj.keys(),10))
                report['root_entries'].append(entry)
        report['status']='ok'
    except (OSError,ValueError,RuntimeError) as e:report['error']=str(e)[:300]
    report.update(remote_size_bytes=args.size,range_bytes_read=source.downloaded,
                  limit_bytes=args.max_mib*1024*1024,
                  warning='Remote ranges are not a verified complete file and do not establish rights or training readiness')
    with open(args.out,'w',encoding='utf-8') as f:json.dump(report,f,ensure_ascii=False,indent=2)
    print(json.dumps(report,ensure_ascii=False,indent=2))


if __name__=='__main__':main()
