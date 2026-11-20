"""Offline tests for bounded HTTP range access."""
import io
import unittest
from unittest.mock import patch

try:
    from inspect_remote_hdf5 import RangeReader
    AVAILABLE=True
except ImportError:
    AVAILABLE=False


@unittest.skipUnless(AVAILABLE,'h5py not installed')
class RangeTests(unittest.TestCase):
    def test_exact_ranges_and_cache(self):
        class Response(io.BytesIO):
            status=206
            headers={'Content-Range':'bytes 0-3/8'}
        with patch('inspect_remote_hdf5.urllib.request.urlopen',return_value=Response(b'abcd')) as call:
            reader=RangeReader('https://example.invalid',8,4,block=4)
            target=bytearray(2)
            self.assertEqual(reader.readinto(target),2);self.assertEqual(bytes(target),b'ab')
            reader.seek(0);self.assertEqual(reader.readinto(target),2)
            self.assertEqual(call.call_count,1);self.assertEqual(reader.downloaded,4)
            reader.seek(4)
            with self.assertRaisesRegex(ValueError,'limit'):reader.readinto(target)

    def test_rejects_range_response_mismatch(self):
        class Response(io.BytesIO):
            status=200
            headers={'Content-Range':'bytes 0-3/8'}
        with patch('inspect_remote_hdf5.urllib.request.urlopen',return_value=Response(b'abcd')):
            reader=RangeReader('https://example.invalid',8,8,block=4)
            with self.assertRaisesRegex(ValueError,'Range'):reader.readinto(bytearray(2))


if __name__=='__main__':unittest.main()
