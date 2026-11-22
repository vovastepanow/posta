"""Offline tests for Posta visual contact sheet generation."""
from pathlib import Path
import tempfile
import unittest

from PIL import Image

from benchmark_embed import write_jsonl
from visual_contact_sheet import generate, safe_image_path, text_lines


class ContactSheetTests(unittest.TestCase):
    def test_sheet_from_valid_captured_image_without_network(self):
        with tempfile.TemporaryDirectory() as td:
            folder = Path(td) / "visual"
            media = folder / "media"
            media.mkdir(parents=True)
            name = "a" * 32 + ".jpg"
            Image.new("RGB", (40, 20), "red").save(media / name)
            write_jsonl(folder / "snapshot.jsonl", [
                {"id": "test", "source_key": "telegram:example", "platform": "Telegram",
                 "data": {"text": "Кот на красном диване", "image_local": f"media/{name}"}}
            ])
            self.assertEqual(safe_image_path({"data": {"image_local": f"media/{name}"}}, folder),
                             (media / name).resolve())
            path, used = generate(folder)
            self.assertEqual(used, 1)
            with Image.open(path) as sheet:
                self.assertEqual(sheet.format, "JPEG")
                self.assertEqual(sheet.width, 1308)
            self.assertTrue(path.exists())

    def test_rejects_media_escape_and_external_url(self):
        with tempfile.TemporaryDirectory() as td:
            folder = Path(td)
            self.assertIsNone(safe_image_path({"data": {"image_local": "../secret"}}, folder))
            self.assertIsNone(safe_image_path({"data": {"image_local": "https://example.com/a.jpg"}}, folder))
            (folder / "media").mkdir()
            secret = folder / ("b" * 32 + ".jpg")
            secret.write_bytes(b"private")
            (folder / "media" / secret.name).symlink_to(secret)
            self.assertIsNone(safe_image_path({"data": {"image_local": "media/" + secret.name}}, folder))

    def test_unicode_wrap_and_empty(self):
        self.assertIn("нет содержательной", text_lines(""))
        self.assertLessEqual(len(text_lines("слово " * 100)), 2)
        self.assertEqual(text_lines("Мем с котом"), ["Мем с котом"])

if __name__ == "__main__":
    unittest.main()
