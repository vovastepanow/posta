"""Test global merge/deduplication with tiny local sources."""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path

try:
    from merge_candidates import merge
    from verify_candidate import verify
    HAVE_DEPS=True
except ImportError:
    HAVE_DEPS=False


@unittest.skipUnless(HAVE_DEPS,'pyarrow/Pillow not installed')
class MergeTests(unittest.TestCase):
    def test_merge_deduplicates_across_sources_and_verifies(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);inputs=[]
            for name in ('a','b'):
                d=root/name;d.mkdir();inputs.append(d)
                row={'dataset_id':name,'record_id':'1','kind':'text','language':'ru',
                     'text':'одинаковый документ','group_id':'1','training_approved':False}
                data=(json.dumps(row,ensure_ascii=False)+'\n').encode();(d/'text.jsonl').write_bytes(data)
                source={'repository':'x/y','revision':'rev','source_path':'train','expected_sha256':'abc','expected_bytes':3}
                (d/'manifest.json').write_text(json.dumps({'sources':{name:source},
                    'files':[{'path':'text.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            result=merge(inputs,root/'out')
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['exact_duplicates_removed'],1)
            self.assertEqual(verify(root/'out')['records'],1)


if __name__=='__main__':unittest.main()
