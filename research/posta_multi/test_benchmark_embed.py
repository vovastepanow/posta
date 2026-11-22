"""No-database, no-model, no-network regression tests for benchmark_embed."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import benchmark_embed as b


class BenchmarkEmbedTests(unittest.TestCase):
    def test_post_id_is_scoped_to_source_and_unambiguous(self):
        self.assertNotEqual(b.post_id("a/b", "c"), b.post_id("a", "b/c"))
        self.assertEqual(json.loads(b.post_id("a", "b")), ["public", "a", "b"])

    def test_local_cache_url_and_gallery_without_network_or_traversal(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            name = "a" * 32 + ".jpg"
            (root / name).write_bytes(b"some cached file")
            record = {"image_local": "https://example.com/image.jpg",
                      "gallery": [{"image_local": "media_cache/" + name}]}
            image, video = b.local_media(record, root, "media_cache")
            self.assertEqual(image, str(root / name))
            self.assertIsNone(video)
            record["gallery"] = [{"image_local": "media_cache/../secrets.jpg"}]
            self.assertEqual(b.local_media(record, root, "media_cache"), (None, None))
            record["gallery"] = [{"image_local": "other/" + name}]
            self.assertEqual(b.local_media(record, root, "media_cache"), (None, None))

    def test_local_symlink_escape_not_resolved(self):
        with tempfile.TemporaryDirectory() as td, tempfile.TemporaryDirectory() as outside:
            secret = Path(outside) / ("a" * 32 + ".jpg")
            secret.write_bytes(b"hidden")
            (Path(td) / secret.name).symlink_to(secret)
            self.assertEqual(b.local_media({"image_local": "media_cache/" + secret.name},
                                           Path(td), "media_cache"), (None, None))

    def test_content_input_uses_native_joint_embedding(self):
        with tempfile.TemporaryDirectory() as tmp:
            name = "f" * 32 + ".png"
            (Path(tmp) / name).write_bytes(b"fake")
            row = {"data": {"text": "кот за компьютером",
                            "image_local": "media_cache/" + name}}
            payload, kind = b.content_input(row, Path(tmp), "media_cache", False)
            self.assertEqual(kind, "image+text")
            self.assertIn("<|image|>", payload["text"])
            self.assertTrue(payload["image"].endswith(name))
            self.assertEqual(b.content_input({"data": {"text": "пост"}}, Path(tmp),
                                             "media_cache", False), ("пост", "text"))

    def test_evaluation_skips_unjudged_and_checks_corpus(self):
        ranked = {"кот": ["a", "c", "b"]}
        incomplete = [{"query": "кот", "relevant_ids": ["a"], "judged_complete": False}]
        result = b.evaluate_rankings(incomplete, ranked, {"a", "b", "c"})
        self.assertIsNone(result["ndcg_at_10"])
        self.assertEqual(result["evaluated_queries"], 0)
        complete = [{"query": "кот", "relevant_ids": ["a", "b"], "judged_complete": True}]
        good = b.evaluate_rankings(complete, ranked, {"a", "b", "c"})
        self.assertEqual(good["recall_at_100"], 1)
        self.assertLess(good["ndcg_at_10"], 1)
        with self.assertRaisesRegex(ValueError, "absent"):
            b.evaluate_rankings(complete, ranked, {"a"})

    def test_top_100_and_ndcg(self):
        ranked = ["non"] * 10 + [str(i) for i in range(200)]
        scores = b.metric_values(ranked, {"0", "110"})
        self.assertEqual(scores["recall_at_100"], 0.5)
        self.assertEqual(scores["ndcg_at_10"], 0)

    def test_template_does_not_overwrite_labels(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "gold.jsonl"
            b.label_template(path)
            records = b.read_jsonl(path)
            self.assertEqual(len(records), 24)
            self.assertFalse(records[0]["judged_complete"])
            b.write_jsonl(path, [{"query": "checked", "relevant_ids": ["yes"], "judged_complete": True}])
            b.label_template(path)
            self.assertEqual(b.read_jsonl(path)[0]["query"], "checked")

    def test_readonly_db_transaction(self):
        class Cursor:
            def __init__(self):
                self.sql = []
            def execute(self, sql, params=None):
                self.sql.append((sql, params))
            def fetchall(self):
                return [("rss:abc", "p1", "rss", {"text": "example"}, ["topic"])]
            def __enter__(self):
                return self
            def __exit__(self, *args):
                return False
        class Connection:
            def __init__(self):
                self.cursor_instance = Cursor()
                self.session = None
                self.rolled_back = False
                self.closed = False
            def set_session(self, **kwargs):
                self.session = kwargs
            def cursor(self):
                return self.cursor_instance
            def rollback(self):
                self.rolled_back = True
            def close(self):
                self.closed = True
        c = Connection()
        import sys
        fake_driver = mock.Mock(connect=mock.Mock(return_value=c))
        with mock.patch.dict(sys.modules, {"psycopg2": fake_driver}):
            records = b.export_public_posts("dummy-dsn", 42)
        self.assertEqual(c.session, {"readonly": True, "autocommit": False})
        self.assertTrue(c.rolled_back)
        self.assertTrue(c.closed)
        sql, params = c.cursor_instance.sql[-1]
        self.assertIn("FROM public_posts AS p", sql)
        self.assertEqual(params, (42,))
        self.assertEqual(records[0]["id"], b.post_id("rss:abc", "p1"))
        self.assertEqual(records[0]["existing_topic_hints"], ["topic"])


if __name__ == "__main__":
    unittest.main()
