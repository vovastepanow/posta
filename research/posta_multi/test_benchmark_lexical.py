"""Offline tests for dependency-free Posta BM25 benchmark."""
import json
from pathlib import Path
import tempfile
import unittest

import benchmark_lexical as b
from benchmark_embed import write_jsonl, post_id


class LexicalBenchmarkTests(unittest.TestCase):
    def test_russian_tokens_and_bm25_ranking(self):
        self.assertEqual(b.tokenize("Формула-1, финансовые новости!"), ["формула", "1", "финансовые", "новости"])
        index = b.build_bm25(["кот за компьютером", "спортивная машина", "компьютер и кот"])
        scores = b.score_bm25("кот", index)
        self.assertGreater(scores[0], scores[1])
        self.assertGreater(scores[2], scores[1])
        self.assertEqual(b.score_bm25("несуществующее", index), [0, 0, 0])

    def test_snapshot_baseline_without_gold_does_not_invent_quality(self):
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            ids = [post_id("rss:site", str(i)) for i in range(3)]
            posts = [
                {"id": ids[0], "platform": "rss", "data": {"text": "кот за компьютером"}},
                {"id": ids[1], "platform": "tg", "data": {"text": "новости формулы один"}},
                {"id": ids[2], "platform": "tg", "data": {"text": "", "image_local": "media_cache/x"}},
            ]
            write_jsonl(folder / "snapshot.jsonl", posts)
            write_jsonl(folder / "labels_template.jsonl", [
                {"query": "кот за компьютером", "relevant_ids": [], "judged_complete": False},
            ])
            result = b.run(folder, None, 2)
            self.assertEqual(result["snapshot_posts"], 3)
            self.assertEqual(result["no_text_posts"], 1)
            self.assertIsNone(result["recall_at_100"])
            self.assertIsNone(result["ndcg_at_10"])
            self.assertEqual(result["evaluated_queries"], 0)
            candidate = json.loads((folder / "lexical_candidates.jsonl").read_text().splitlines()[0])
            self.assertEqual(candidate["post_id"], ids[0])
            self.assertEqual(candidate["rank"], 1)

    def test_independently_judged_gold_can_be_evaluated(self):
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            ids = [post_id("rss:x", str(i)) for i in range(2)]
            write_jsonl(folder / "snapshot.jsonl", [
                {"id": ids[0], "platform": "rss", "data": {"text": "кот"}},
                {"id": ids[1], "platform": "rss", "data": {"text": "космос"}},
            ])
            gold = folder / "gold.jsonl"
            write_jsonl(gold, [{"query": "кот", "relevant_ids": [ids[0]], "judged_complete": True}])
            result = b.run(folder, gold, 0)
            self.assertEqual(result["recall_at_100"], 1.0)
            self.assertEqual(result["ndcg_at_10"], 1.0)


if __name__ == "__main__":
    unittest.main()
