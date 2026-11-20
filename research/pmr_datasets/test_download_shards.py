"""Verify source shard metadata and local integrity rules offline."""
import hashlib
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from download_shards import metadata, sha256_file


class DownloadTests(unittest.TestCase):
    def test_metadata_requires_exact_path_and_lfs_digest(self):
        with patch('download_shards.get_json', return_value=[{'type': 'file', 'path': 'dir/a',
                    'size': 3, 'lfs': {'oid': 'a' * 64}}]):
            self.assertEqual(metadata('owner/repo', 'rev', 'dir/a'), (3, 'a' * 64))
            with self.assertRaises(ValueError):
                metadata('owner/repo', 'rev', 'dir/b')
        with patch('download_shards.get_json', return_value=[{'type': 'file', 'path': 'dir/a', 'size': 3}]):
            with self.assertRaises(ValueError):
                metadata('owner/repo', 'rev', 'dir/a')

    def test_file_sha256(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / 'data'
            path.write_bytes(b'abc')
            self.assertEqual(sha256_file(path), hashlib.sha256(b'abc').hexdigest())


if __name__ == '__main__':
    unittest.main()
