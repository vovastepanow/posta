"""Exercise video extraction using a locally generated synthetic clip."""
import csv
import hashlib
import json
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path
from prepare_videoufo import build


@unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'ffmpeg/ffprobe unavailable')
class VideoTests(unittest.TestCase):
    def test_verified_archive_and_caption_are_combined(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);inputs=root/'input';inputs.mkdir()
            clip=root/'clip.mp4'
            subprocess.run(['ffmpeg','-v','error','-f','lavfi','-i','color=c=red:s=32x32:r=4',
                            '-t','0.5','-c:v','mpeg4',str(clip)],check=True,capture_output=True)
            archive=inputs/'video.tar'
            with tarfile.open(archive,'w') as t:t.add(clip,arcname='VideoUFO/abc.0.mp4')
            csvfile=inputs/'annotations.csv'
            with csvfile.open('w',encoding='utf-8',newline='') as f:
                writer=csv.DictWriter(f,fieldnames=['ID','Brief Caption','Detailed Caption','Start Time','End Time'])
                writer.writeheader();writer.writerow({'ID':'abc.0','Brief Caption':'a red square',
                                                       'Detailed Caption':'a red square in motion',
                                                       'Start Time':'00:00:01','End Time':'00:00:02'})
            records={}
            for id_,path in [('videoufo',archive),('videoufo-annotations',csvfile)]:
                data=path.read_bytes()
                records[id_]={'status':'verified','local_path':str(path),'expected_bytes':len(data),
                              'expected_sha256':hashlib.sha256(data).hexdigest(),'repository':'x/y',
                              'revision':'rev','source_path':path.name}
            (inputs/'manifest-a.json').write_text(json.dumps({'records':records}))
            result=build(inputs,root/'output',1)
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['matched_annotations'],1)
            self.assertEqual(result['rejections'],{})
            try:
                from merge_candidates import merge
                from verify_candidate import verify
            except ImportError:
                return
            merged=merge([root/'output'],root/'merged')
            self.assertEqual(merged['accepted'],1)
            self.assertEqual(verify(root/'merged',decode_media=True)['media_files'],1)


if __name__=='__main__':unittest.main()
