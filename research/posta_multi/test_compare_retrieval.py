"""Regression tests for Posta neural-versus-BM25 readable report."""
import json
from pathlib import Path
import tempfile
import unittest

from benchmark_embed import write_jsonl
from compare_retrieval import report


class CompareRetrievalTests(unittest.TestCase):
    def make_fixture(self, folder: Path, *, partial=False):
        count = 20
        (folder / "metrics.json").write_text(json.dumps({
            "text_only": True,
            "snapshot_rows": count,
            "selected_rows": 5 if partial else count,
            "indexed": 5 if partial else count,
            "queries": 1,
        }), encoding="utf-8")
        (folder / "lexical_metrics.json").write_text(json.dumps({
            "snapshot_posts": count,
            "queries": 1,
        }), encoding="utf-8")
        write_jsonl(folder / "candidates.jsonl", [
            {"query": "Кот за компьютером", "rank": i + 1,
             "post_id": str(i), "score_cosine": 0.9 - i * .01,
             "text_preview": "Кот спит на клавиатуре" if i == 0 else "Тест"}
            for i in range(20)
        ])
        write_jsonl(folder / "lexical_candidates.jsonl", [
            {"query": "Кот за компьютером", "rank": i + 1,
             "post_id": str(19 - i), "score_bm25": 10 - i,
             "text_preview": "Про новости   \n    и события"}
            for i in range(20)
        ])

    def test_generates_readable_report_without_quality_claim(self):
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            self.make_fixture(folder)
            result = report(folder, 1, 3)
            self.assertIn("EmbeddingGemma 2", result)
            self.assertIn("BM25", result)
            self.assertIn("Оценки релевантности пока не размечены вручную", result)
            self.assertIn("Общие результаты в топ-10: **0/10**", result)
            self.assertIn("Про новости и события", result)
            self.assertIn("Кот спит на клавиатуре", result)

    def test_rejects_partial_smoke_comparison(self):
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            self.make_fixture(folder, partial=True)
            with self.assertRaisesRegex(ValueError, "full text-only snapshot"):
                report(folder, 1, 3)

    def test_rejects_malformed_candidates(self):
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp)
            self.make_fixture(folder)
            rows = [
                {"query": "Кот за компьютером", "rank": 1,
                 "post_id": "same", "text_preview": "duplicate"},
                {"query": "Кот за компьютером", "rank": 2,
                 "post_id": "same", "text_preview": "duplicate"},
            ]
            write_jsonl(folder / "candidates.jsonl", rows)
            with self.assertRaisesRegex(ValueError, "Duplicate"):
                report(folder, 1, 3)


if __name__ == "__main__":
    unittest.main()
