"""Tests for isolated Posta visual benchmark dataset preparation (no network)."""
import json
from io import BytesIO
from pathlib import Path
import tempfile
import unittest

from PIL import Image

from benchmark_embed import content_input, read_jsonl, write_jsonl
import prepare_visual_smoke as p


def jpg_bytes(color):
    b = BytesIO()
    Image.new("RGB", (40, 20), color).save(b, format="JPEG")
    return b.getvalue()


class VisualSmokeTests(unittest.TestCase):
    def test_remote_url_allowlist(self):
        self.assertTrue(p.allowed_url("https://cdn4.telesco.pe/image.jpg?x=1"))
        self.assertTrue(p.allowed_url("https://sun9-84.vkuserphoto.ru/test.png"))
        for value in (
            "http://cdn4.telesco.pe/image.jpg",
            "https://evil.test/image.jpg",
            "https://localhost/path",
            "https://127.0.0.1/path",
            "https://cdn4.telesco.pe:8443/image.jpg",
            "https://name:secret@cdn4.telesco.pe/path",
            "file:///etc/passwd",
            "https://cdn4.telesco.pe/image.jpg#frag",
            "https://[bad",
        ):
            self.assertFalse(p.allowed_url(value), value)

    def test_normalized_jpeg_and_size_cap(self):
        img = p.normalized_jpg(jpg_bytes("red"))
        with Image.open(BytesIO(img)) as decoded:
            self.assertEqual(decoded.format, "JPEG")
            self.assertEqual(decoded.size, (40, 20))
        with self.assertRaises(Exception):
            p.normalized_jpg(b"not an image")

    def test_balanced_sample_and_original_preserved(self):
        with tempfile.TemporaryDirectory() as t:
            root = Path(t)
            source = root / "original" / "snapshot.jsonl"
            rows = [
                {"id": f"post{i}", "source_key": src, "platform": "Telegram",
                 "data": {"text": "Пост без текста", "image": f"https://cdn4.telesco.pe/{i}.jpg"}}
                for i, src in enumerate(["a", "a", "b", "b", "c"])
            ]
            write_jsonl(source, rows)
            initial = source.read_bytes()
            images = {str(i): jpg_bytes(color)
                      for i, color in enumerate(["red", "green", "blue", "yellow", "magenta"])}
            seen = []
            def fake_download(url, timeout):
                seen.append(url)
                return images[url.rsplit("/", 1)[-1].split(".")[0]]
            dest = root / "isolated" / "visual"
            result = p.prepare(source, dest, limit=3, attempts=5,
                               timeout=3, fetcher=fake_download)
            self.assertEqual(result["image_posts"], 3)
            self.assertEqual(result["distinct_sources"], 3)
            self.assertEqual(source.read_bytes(), initial)
            posts = read_jsonl(dest / "snapshot.jsonl")
            self.assertEqual(len(posts), 3)
            self.assertEqual({x["source_key"] for x in posts}, {"a", "b", "c"})
            for row in posts:
                self.assertEqual(row["data"]["text"], "")
                payload, kind = content_input(row, dest / "media", "media", False)
                self.assertEqual(kind, "image")
                self.assertTrue(Path(payload["image"]).is_file())
            self.assertEqual(len(read_jsonl(dest / "labels_template.jsonl")), 24)

    def test_no_download_does_not_write_snapshot(self):
        with tempfile.TemporaryDirectory() as t:
            root = Path(t)
            source = root / "snapshot.jsonl"
            write_jsonl(source, [
                {"id": "p", "source_key": "a", "data": {
                    "text": "test", "image": "https://cdn4.telesco.pe/one.jpg"}}
            ])
            dest = root / "visual"
            with self.assertRaisesRegex(RuntimeError, "No media downloaded"):
                p.prepare(source, dest, 1, 1, 1, fetcher=lambda *_: b"not an image")
            self.assertFalse((dest / "snapshot.jsonl").exists())

    def test_does_not_overwrite_original(self):
        with tempfile.TemporaryDirectory() as t:
            folder = Path(t)
            src = folder / "snapshot.jsonl"
            write_jsonl(src, [{"id": "p", "data": {}}])
            with self.assertRaises(ValueError):
                p.prepare(src, folder, 1, 1, 1, fetcher=lambda *_: b"")


if __name__ == "__main__":
    unittest.main()
