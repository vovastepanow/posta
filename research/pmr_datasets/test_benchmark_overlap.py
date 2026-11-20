"""Offline regression test for exact benchmark contamination checks."""
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
try:
    import pyarrow as pa
    import pyarrow.parquet as pq
    from check_benchmark_overlap import check
    AVAILABLE=True
except ImportError:
    AVAILABLE=False


@unittest.skipUnless(AVAILABLE,'pyarrow not installed')
class BenchmarkTests(unittest.TestCase):
    def test_detects_exact_normalized_overlap_and_requires_isolation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);candidate=root/'candidate';candidate.mkdir();source=root/'source';source.mkdir()
            row={'dataset_id':'a','record_id':'1','split':'train','text':' SAME   example ','positive':'','negatives':[]}
            data=(json.dumps(row)+'\n').encode();(candidate/'text.jsonl').write_bytes(data)
            (candidate/'manifest.json').write_text(json.dumps({'files':[{'path':'text.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            records={}
            ids=['mteb-ria-eval-queries','mteb-ria-eval-qrels']+[f'mteb-ria-eval-corpus-{i:02d}' for i in range(4)]
            for i,id_ in enumerate(ids):
                path=source/(str(i)+'.parquet')
                pq.write_table(pa.table({'text':['same example' if i==0 else 'different text']}),path)
                content=path.read_bytes()
                records[id_]={'status':'verified','evaluation_only':True,'local_path':str(path),
                              'expected_bytes':len(content),'expected_sha256':hashlib.sha256(content).hexdigest()}
            (source/'manifest-k.json').write_text(json.dumps({'records':records}))
            result=check(candidate,source)
            self.assertEqual(result['matched_distinct_texts'],1)
            row['text']='different source document';row['ocr']='same example'
            data=(json.dumps(row)+'\n').encode();(candidate/'text.jsonl').write_bytes(data)
            (candidate/'manifest.json').write_text(json.dumps({'files':[{'path':'text.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            ocr_result=check(candidate,source)
            self.assertEqual(ocr_result['matched_distinct_texts'],1)
            self.assertEqual(ocr_result['matches'][0]['candidate_occurrences'][0]['role'],'ocr')
            row['ocr']='different text';row['ocr_annotations']={'word_bboxes':[{'text':'same example'}]}
            data=(json.dumps(row)+'\n').encode();(candidate/'text.jsonl').write_bytes(data)
            (candidate/'manifest.json').write_text(json.dumps({'files':[{'path':'text.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            nested=check(candidate,source)
            self.assertEqual(nested['matches'][0]['candidate_occurrences'][0]['role'],'ocr_annotation_word_bboxes')
            records[ids[0]]['evaluation_only']=False
            (source/'manifest-k.json').write_text(json.dumps({'records':records}))
            with self.assertRaisesRegex(ValueError,'not isolated'):
                check(candidate,source)


if __name__=='__main__':unittest.main()
