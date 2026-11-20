"""Test extraction of a small synthetic HDF5 sample offline."""
import hashlib
import io
import json
import tempfile
import unittest
from pathlib import Path

try:
    import h5py
    import numpy as np
    from PIL import Image
    from prepare_ocr import build
    HAVE_DEPS=True
except ImportError:
    HAVE_DEPS=False


@unittest.skipUnless(HAVE_DEPS,'h5py, numpy or Pillow not installed')
class OcrTests(unittest.TestCase):
    def test_verified_ocr_sample(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);src=root/'src';src.mkdir();h5=src/'sample.h5'
            image=Image.new('RGB',(16,8),'white');buf=io.BytesIO();image.save(buf,format='JPEG')
            with h5py.File(h5,'w') as f:
                d=f.create_dataset('images',(1,),dtype=h5py.vlen_dtype(np.dtype('uint8')))
                d[0]=np.frombuffer(buf.getvalue(),dtype=np.uint8)
                f.create_dataset('labels',data=[b'A'])
                f.create_dataset('annotations',data=[b'{"word_bboxes": []}'])
                f.create_dataset('dimensions',data=[[16,8]])
                f.create_dataset('qualities',data=[100]);f.create_dataset('sample_ids',data=[3])
            data=h5.read_bytes();entry={'status':'verified','local_path':str(h5),'expected_bytes':len(data),
                                      'expected_sha256':hashlib.sha256(data).hexdigest(),
                                      'revision':'rev','source_path':'ru/train.h5','repository':'x/y'}
            (src/'manifest-a.json').write_text(json.dumps({'records':{'nvidia-ocr-ru':entry}}))
            result=build(src,root/'out',1)
            self.assertEqual(result['accepted'],1)
            self.assertEqual(result['rejections'],{})
            self.assertEqual(result['counts'][0]['language'],'ru')


if __name__=='__main__':unittest.main()
