"""CPU-only, offline tests for frozen human-judged evaluation."""
import tempfile
import unittest
from pathlib import Path
from urllib.parse import parse_qs, urlparse
from miracl_benchmark import checked_row, download_dev, evaluate, freeze, run


def row(i, lang):
    return {"query_id": str(i), "query": f"query topic {i} {lang}",
            "positive_passages": [{"docid": f"p{i}", "title": "topic",
                                   "text": f"query topic {i} with useful details"}],
            "negative_passages": [{"docid": f"n{i}", "title": "other",
                                   "text": f"unrelated vegetables {i} and sunshine"}]}


class MiraclTests(unittest.TestCase):
    def test_schema_and_conflicting_labels(self):
        self.assertIsNotNone(checked_row(row(1, "ru"), "ru"))
        bad = row(1, "ru")
        bad["negative_passages"][0]["docid"] = "p1"
        self.assertIsNone(checked_row(bad, "ru"))
        bad["negative_passages"] = []
        self.assertIsNone(checked_row(bad, "ru"))

    def test_stable_freeze_and_minimum(self):
        rows = [row(i, "ru") for i in range(30)]
        a, stats = freeze(rows, "ru", 9)
        b, _ = freeze(list(reversed(rows)), "ru", 9)
        self.assertEqual(a, b)
        self.assertEqual(stats["usable_queries"], 30)
        with self.assertRaises(ValueError):
            freeze(rows, "ru", 99)

    def test_bm25_aggregate_contains_no_raw_text(self):
        groups = {lang: freeze([row(i, lang) for i in range(9)],
                               lang, 5)[0] for lang in ("ru", "en")}
        result = evaluate(groups, "a"*40, {})
        self.assertEqual(result["overall"]["queries"], 10)
        self.assertGreater(result["overall"]["pair_accuracy"], .5)
        self.assertEqual(len(result["frozen_data_sha256"]), 64)
        self.assertNotIn("query topic", str(result))

    def test_paging_and_incomplete_rows_rejected(self):
        data = [row(i, "en") for i in range(5)]
        def fetch(url):
            params = parse_qs(urlparse(url).query)
            start, length = int(params["offset"][0]), int(params["length"][0])
            return {"num_rows_total": 5,
                    "rows": [{"row": r} for r in data[start:start+length]]}
        self.assertEqual(download_dev("en", fetch, page_size=2), data)
        with self.assertRaises(ValueError):
            download_dev("en", lambda _: {"num_rows_total": 5, "rows": []},
                         page_size=2)

    def test_local_files_and_revision_check(self):
        def fetch(url):
            if "/api/datasets/" in url:
                return {"sha": "a"*40, "private": False, "gated": False}
            params = parse_qs(urlparse(url).query)
            language = params["config"][0]
            start, length = int(params["offset"][0]), int(params["length"][0])
            data = [row(i, language) for i in range(8)]
            return {"num_rows_total": 8,
                    "rows": [{"row": r} for r in data[start:start+length]]}
        with tempfile.TemporaryDirectory() as temp:
            output, source = Path(temp)/"report.json", Path(temp)/"private.jsonl"
            result = run(output, source, 4, fetch)
            self.assertEqual(result["overall"]["queries"], 8)
            self.assertEqual(len(source.read_text().splitlines()), 8)
            with self.assertRaises(FileExistsError):
                run(output, source, 4, fetch)


if __name__ == "__main__":
    unittest.main()
