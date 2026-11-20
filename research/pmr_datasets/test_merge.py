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
    def test_reject_evaluation_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);d=root/'eval';d.mkdir()
            row={'dataset_id':'eval','record_id':'1','kind':'text','language':'ru',
                 'text':'evaluation data','group_id':'1','training_approved':False}
            data=(json.dumps(row)+'\n').encode();(d/'data.jsonl').write_bytes(data)
            source={'repository':'VLM2Vec/MMEB-V3','revision':'rev','source_path':'test',
                    'expected_sha256':'abc','expected_bytes':3}
            (d/'manifest.json').write_text(json.dumps({'sources':{'eval':source},
                'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            with self.assertRaisesRegex(ValueError,'evaluation source'):
                merge([d],root/'out')

    def test_benchmark_exclusion_drops_matching_rows_and_rejects_wrong_report(self):
        with tempfile.TemporaryDirectory() as tmp:
            from prepare import sha
            root=Path(tmp);d=root/'source';d.mkdir()
            rows=[{'dataset_id':'a','record_id':'1','kind':'text','language':'ru',
                   'text':'совпавший текст','group_id':'1','training_approved':False},
                  {'dataset_id':'a','record_id':'2','kind':'text','language':'ru',
                   'text':'другой документ','group_id':'2','training_approved':False}]
            data=''.join(json.dumps(r,ensure_ascii=False)+'\n' for r in rows).encode()
            (d/'data.jsonl').write_bytes(data)
            source={'repository':'x/y','revision':'rev','source_path':'train','expected_sha256':'abc','expected_bytes':3}
            (d/'manifest.json').write_text(json.dumps({'sources':{'a':source},
                'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            report=root/'report.json'
            report.write_text(json.dumps({'benchmark':'mteb/RiaNewsRetrieval',
                'candidate_manifest_sha256':hashlib.sha256((d/'manifest.json').read_bytes()).hexdigest(),
                'matches':[{'text_sha256':sha('совпавший текст')}]}))
            result=merge([d],root/'out',report)
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['benchmark_exclusions']['rows'],1)
            self.assertEqual(verify(root/'out')['records'],1)
            report.write_text(json.dumps({'benchmark':'mteb/RiaNewsRetrieval',
                                          'candidate_manifest_sha256':'wrong','matches':[]}))
            with self.assertRaisesRegex(ValueError,'does not match'):
                merge([d],root/'out2',report)

    def test_benchmark_negative_is_removed_not_the_pair(self):
        with tempfile.TemporaryDirectory() as tmp:
            from prepare import sha
            root=Path(tmp);d=root/'source';d.mkdir()
            row={'dataset_id':'a','record_id':'1','kind':'retrieval','language':'ru',
                 'text':'поисковый запрос','positive':'верный документ','negatives':['тестовый документ'],
                 'group_id':'1','training_approved':False}
            data=(json.dumps(row,ensure_ascii=False)+'\n').encode();(d/'data.jsonl').write_bytes(data)
            source={'repository':'x/y','revision':'rev','source_path':'train','expected_sha256':'abc','expected_bytes':3}
            (d/'manifest.json').write_text(json.dumps({'sources':{'a':source},
                'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            report=root/'report.json';report.write_text(json.dumps({'benchmark':'mteb/RiaNewsRetrieval',
                'candidate_manifest_sha256':hashlib.sha256((d/'manifest.json').read_bytes()).hexdigest(),
                'matches':[{'text_sha256':sha('тестовый документ')}]}))
            result=merge([d],root/'out',report)
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['benchmark_exclusions']['negatives'],1)
            self.assertEqual(verify(root/'out')['records'],1)

    def test_invalid_example_is_reported_and_valid_example_survives(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);d=root/'source';d.mkdir()
            rows=[{'dataset_id':'a','record_id':'bad','kind':'retrieval','language':'ru',
                   'text':'а','positive':'верный документ','group_id':'1','training_approved':False},
                  {'dataset_id':'a','record_id':'ok','kind':'text','language':'ru',
                   'text':'другой документ','group_id':'2','training_approved':False}]
            data=''.join(json.dumps(r,ensure_ascii=False)+'\n' for r in rows).encode()
            (d/'data.jsonl').write_bytes(data)
            source={'repository':'x/y','revision':'rev','source_path':'train','expected_sha256':'abc','expected_bytes':3}
            (d/'manifest.json').write_text(json.dumps({'sources':{'a':source},
                'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            result=merge([d],root/'out')
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['rejections_report']['rows'],1)
            self.assertEqual(verify(root/'out')['records'],1)

    def test_replace_source_keeps_only_designated_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);inputs=[]
            for name,text in [('old','устаревший документ'),('new','новый документ')]:
                d=root/name;d.mkdir();inputs.append(d)
                row={'dataset_id':'a','record_id':name,'kind':'text','language':'ru',
                     'text':text,'group_id':name,'training_approved':False}
                data=(json.dumps(row,ensure_ascii=False)+'\n').encode();(d/'data.jsonl').write_bytes(data)
                source={'repository':'x/y','revision':'rev','source_path':'train','expected_sha256':'abc','expected_bytes':3}
                if name=='old':source['license_review_status']='pending'
                (d/'manifest.json').write_text(json.dumps({'sources':{'a':source},
                    'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            result=merge(inputs,root/'out',replacements={'a':inputs[1]})
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['superseded_source_rows']['a'],1)
            self.assertEqual(verify(root/'out')['records'],1)

    def test_invalid_video_timestamp_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);d=root/'video';d.mkdir()
            row={'dataset_id':'v','record_id':'1','kind':'video_text','language':'en',
                 'text':'video description','group_id':'1','media_sha256':'a'*64,
                 'source_start_time':'invalid','source_end_time':'0:00:03',
                 'duration_seconds':'3','training_approved':False}
            data=(json.dumps(row)+'\n').encode();(d/'data.jsonl').write_bytes(data)
            source={'repository':'x/y','revision':'rev','source_path':'video','expected_sha256':'abc','expected_bytes':3}
            (d/'manifest.json').write_text(json.dumps({'sources':{'v':source},
                'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            result=merge([d],root/'out')
            self.assertEqual(result['accepted'],0)
            self.assertEqual(result['rejections_report']['rows'],1)

    def test_reject_conflicting_source_revisions(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);inputs=[]
            for name,revision in [('one','old'),('two','new')]:
                d=root/name;d.mkdir();inputs.append(d)
                row={'dataset_id':'a','record_id':name,'kind':'text','language':'en',
                     'text':'valid document','group_id':name,'training_approved':False}
                data=(json.dumps(row)+'\n').encode();(d/'data.jsonl').write_bytes(data)
                source={'repository':'x/y','revision':revision,'source_path':'train','expected_sha256':'abc','expected_bytes':3}
                (d/'manifest.json').write_text(json.dumps({'sources':{'a':source},
                    'files':[{'path':'data.jsonl','sha256':hashlib.sha256(data).hexdigest()}]}))
            with self.assertRaisesRegex(ValueError,'conflicting source provenance'):
                merge(inputs,root/'out')

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
            (root/'out'/'duplicates.jsonl').write_text('tampered')
            with self.assertRaisesRegex(ValueError, 'duplicates_report'):
                verify(root/'out')


if __name__=='__main__':unittest.main()
