"""Tests for read-only Posta retrieval snapshot composition audit."""
from pathlib import Path
import tempfile
import unittest

import audit_retrieval_corpus as a


class CorpusAuditTests(unittest.TestCase):
    def test_skew_placeholders_media_and_duplication(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = "a" * 32 + ".jpg"
            (root / name).write_bytes(b"not necessarily a valid image")
            records = [
                {"source_key": "s1", "platform": "telegram",
                 "data": {"text": "Пост без текста", "image_local": f"media/{name}",
                          "description": "Одинаковое длинное описание для ленты и новостей"}},
                {"source_key": "s1", "platform": "telegram",
                 "data": {"text": "Пост без текста", "video_local": f"media/{'b' * 32}.mp4",
                          "description": "Одинаковое длинное описание для ленты и новостей"}},
                {"source_key": "s2", "platform": "rss", "data": {"text": "кот"}},
                {"source_key": "s1", "platform": "telegram", "data": {"text": ""}},
            ]
            result = a.audit(records, root, "media")
            self.assertEqual(result["snapshot_posts"], 4)
            self.assertEqual(result["distinct_sources"], 2)
            self.assertEqual(result["top_source_share_pct"], 75)
            self.assertEqual(result["empty_text"], 1)
            # The repeated long description is part of caption: literal
            # placeholder detection applies to full combined text.
            self.assertEqual(result["literal_placeholder_text"], 0)
            self.assertEqual(result["very_short_nonplaceholder_text_under_6_words"], 1)
            self.assertEqual(result["posts_with_repeated_long_description"], 2)
            self.assertEqual(result["exact_duplicate_captions_extra_posts"], 1)
            self.assertEqual(result["has_image_local_reference"], 1)
            self.assertEqual(result["image_file_accessible"], 1)
            self.assertEqual(result["has_video_local_reference"], 1)
            self.assertEqual(result["video_file_accessible"], 0)

    def test_empty_rows_and_nested_gallery(self):
        with tempfile.TemporaryDirectory() as directory:
            result = a.audit([], Path(directory), "media")
            self.assertEqual(result["snapshot_posts"], 0)
            self.assertIsNone(result["top_source_share_pct"])
            rows = [{"source_key": "s", "platform": "reddit",
                     "data": {"text": "Без текста", "gallery": [
                         {"image_local": "media/" + "a" * 32 + ".png"}
                     ]}}]
            result = a.audit(rows, Path(directory), "media")
            self.assertEqual(result["literal_placeholder_text"], 1)
            self.assertEqual(result["has_image_local_reference"], 1)
            self.assertEqual(result["image_file_accessible"], 0)


if __name__ == "__main__":
    unittest.main()
