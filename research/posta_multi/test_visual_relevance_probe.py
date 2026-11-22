"""Unit tests for human-reviewed visual retrieval probe; no neural model or network."""
from pathlib import Path
import json
import tempfile
import unittest

from benchmark_embed import write_jsonl, read_jsonl
from visual_relevance_probe import ITEMS, checked_rows, gold_rows, prepare, score


class VisualRelevanceProbeTests(unittest.TestCase):
    def fixture(self, folder: Path):
        media = folder / "media"
        media.mkdir(parents=True)
        name = "a" * 32 + ".jpg"
        (media / name).write_bytes(b"fake image file; scoring never decodes it")
        rows = [
            {
                "id": f"post-{i + 1}",
                "source_key": source,
                "platform": "test",
                "data": {
                    "text": "описание из текста" if i < 6 else "",
                    "image_local": "media/" + name,
                },
            }
            for i, (source, _) in enumerate(ITEMS)
        ]
        write_jsonl(folder / "snapshot.jsonl", rows)
        return rows

    def test_prepare_ten_judged_labels_from_twelve_items(self):
        with tempfile.TemporaryDirectory() as td:
            folder = Path(td)
            rows = self.fixture(folder)
            output = prepare(folder)
            labels = read_jsonl(output)
            self.assertEqual(len(labels), 10)
            self.assertTrue(all(g["judged_complete"] for g in labels))
            self.assertNotIn(8, [g["visual_target_number"] for g in labels])
            self.assertNotIn(11, [g["visual_target_number"] for g in labels])
            self.assertEqual(labels[0]["relevant_ids"], [rows[0]["id"]])
            self.assertEqual(checked_rows(folder), rows)

    def test_score_reports_image_only_and_image_plus_text(self):
        with tempfile.TemporaryDirectory() as td:
            folder = Path(td)
            rows = self.fixture(folder)
            labels = gold_rows(rows)
            (folder / "metrics.json").write_text(json.dumps({
                "text_only": False, "selected_rows": 12,
                "indexed": 12, "snapshot_rows": 12,
                "kinds": {"image": 6, "image+text": 6},
                "queries": 10,
            }))
            # Put the true image first for 8 queries and third for 2.
            candidates = []
            for k, item in enumerate(labels):
                target = item["relevant_ids"][0]
                ids = [x["id"] for x in rows if x["id"] != target]
                ids.insert(2 if k >= 8 else 0, target)
                candidates += [
                    {"query": item["query"], "post_id": post_id, "rank": rank}
                    for rank, post_id in enumerate(ids, 1)
                ]
            write_jsonl(folder / "candidates.jsonl", candidates)
            results = score(folder)
            self.assertEqual(results["overall"]["queries"], 10)
            self.assertEqual(results["overall"]["hit_at_1"], .8)
            self.assertEqual(results["overall"]["hit_at_3"], 1.)
            self.assertEqual(results["image_only"]["queries"], 4)
            self.assertEqual(results["image_plus_text"]["queries"], 6)
            self.assertEqual(len(read_jsonl(folder / "visual_gold.jsonl")) if (folder / "visual_gold.jsonl").exists() else 0, 0)

    def test_prevents_reusing_judgments_for_other_snapshot(self):
        with tempfile.TemporaryDirectory() as td:
            folder = Path(td)
            rows = self.fixture(folder)
            rows[0]["source_key"] = "wrong"
            write_jsonl(folder / "snapshot.jsonl", rows)
            with self.assertRaisesRegex(ValueError, "source mismatch"):
                prepare(folder)

    def test_rejects_text_only_benchmark_when_scoring(self):
        with tempfile.TemporaryDirectory() as td:
            folder = Path(td)
            rows = self.fixture(folder)
            labels = gold_rows(rows)
            (folder / "metrics.json").write_text(json.dumps({
                "text_only": True, "selected_rows": 12, "indexed": 12,
                "snapshot_rows": 12, "kinds": {"text": 12},
                "queries": len(labels),
            }))
            write_jsonl(folder / "candidates.jsonl", [
                {"query": g["query"], "post_id": rows[0]["id"], "rank": 1}
                for g in labels
            ])
            with self.assertRaisesRegex(ValueError, "12-image multimodal"):
                score(folder)


if __name__ == "__main__":
    unittest.main()
