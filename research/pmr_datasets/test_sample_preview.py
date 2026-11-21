"""Offline tests: source preview must not invent synthetic hard-negative text."""
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import sample_preview


class NewSourcePreviewTests(unittest.TestCase):
    def test_fineweb2_ir_positive_only_and_unapproved(self):
        dataset = {"id": "fineweb2-ir-ru",
                   "repo": "hotchpotch/FineWeb2-IR",
                   "license_claim": "upstream terms"}
        upstream = {"id": 123, "original_row_id": 17,
                    "query": "Какие бывают облака?",
                    "text": "Облака бывают разных видов.",
                    "hard_negatives": [34, 12, 91]}
        def fetch(url):
            if "first-rows" in url:
                return {"rows": [{"row_idx": 4, "row": upstream}]}
            return {"sha": "a"*40}
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)
            with patch.object(sample_preview, "get_json", side_effect=fetch):
                report = sample_preview.collect(dataset, 1, path, False)
            row = json.loads((path/"fineweb2-ir-ru.jsonl").read_text())
            self.assertEqual(row["text"], upstream["query"])
            self.assertEqual(row["positives"], [upstream["text"]])
            self.assertEqual(row["negatives"], [])
            self.assertFalse(row["training_approved"])
            self.assertEqual(row["hard_negative_ids_reported"], 3)
            self.assertFalse(row["hard_negative_passages_resolved"])
            self.assertEqual(row["annotation_origin"],
                             "synthetic query-crafter-multilingual")
            self.assertEqual(report["media_verified"], 0)
            self.assertEqual(report["revision_binding"].split(";")[0],
                             "HEAD checked before and after")

    def test_edu_preview_uses_source_text_and_retains_origin(self):
        dataset = {"id": "fineweb-edu-en",
                   "repo": "HuggingFaceFW/fineweb-edu",
                   "license_claim": "odc-by-1.0"}
        def fetch(url):
            if "first-rows" in url:
                return {"rows": [{"row_idx": 1, "row": {
                    "id": "CC-document-1", "url": "https://example.org/a",
                    "text": "Educational example text."}}]}
            return {"sha": "b"*40}
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            with patch.object(sample_preview, "get_json", side_effect=fetch):
                sample_preview.collect(dataset, 1, folder, False)
            row = json.loads((folder/"fineweb-edu-en.jsonl").read_text())
            self.assertEqual(row["kind"], "text")
            self.assertEqual(row["language"], "en")
            self.assertEqual(row["record_id"], "CC-document-1")
            self.assertFalse(row["training_approved"])


if __name__ == "__main__":
    unittest.main()
