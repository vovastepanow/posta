#!/usr/bin/env python3
"""Offline contract tests. Run: python3 -m unittest discover -s research/pmr_datasets -p 'test_*.py'"""
import hashlib
import unittest

from prepare import normalize, prepare
from probe import feature_summary


def media_hash(label):
    return hashlib.sha256(label.encode()).hexdigest()


class PmrPilotTests(unittest.TestCase):
    def setUp(self):
        self.catalog = {"datasets": [
            {"id": "a", "training": True},
            {"id": "b", "training": True},
            {"id": "eval", "training": False},
        ]}
        self.audit = {"datasets": [
            {"id": name, "metadata_status": "ok", "revision": "abc123"}
            for name in ("a", "b", "eval")
        ]}
        self.approvals = {
            name: {"approved_for_training": True, "revision": "abc123", "review_note": "manual review"}
            for name in ("a", "b", "eval")
        }

    def example(self, source="a", record="1", text="Красивый автомобиль", kind="text", **extras):
        return {"dataset_id": source, "record_id": record, "kind": kind,
                "language": "ru", "text": text, **extras}

    def splits(self, items):
        return prepare(items, self.catalog, self.audit, self.approvals)

    def test_exact_duplicates_across_sources_removed(self):
        splits, duplicates = self.splits([
            self.example(), self.example(source="b", record="2")
        ])
        self.assertEqual(duplicates, 1)
        self.assertEqual(sum(map(len, splits.values())), 1)

    def test_shared_query_never_crosses_split(self):
        rows = [normalize(self.example(record="x", kind="retrieval",
                                       text="как сменить масло", positive="инструкция первая")),
                normalize(self.example(record="y", kind="retrieval",
                                       text="как сменить масло", positive="инструкция вторая"))]
        from prepare import partition
        groups = partition(rows)
        self.assertEqual(groups[0], groups[1])

    def test_shared_positive_never_crosses_split(self):
        rows = [
            normalize(self.example(record="1", kind="retrieval", text="первая фраза",
                                   positive="один позитивный документ")),
            normalize(self.example(record="2", kind="retrieval", text="вторая фраза",
                                   positive="один позитивный документ")),
        ]
        from prepare import partition
        assignments = partition(rows)
        self.assertEqual(assignments[0], assignments[1])

    def test_media_sha_groups_different_captions(self):
        m = media_hash("same-file")
        rows = [
            normalize(self.example(record="1", text="красная машина", kind="image_text", media_sha256=m)),
            normalize(self.example(record="2", text="алый автомобиль", kind="image_text", media_sha256=m)),
        ]
        from prepare import partition
        assignments = partition(rows)
        self.assertEqual(assignments[0], assignments[1])

    def test_media_rejected_without_bytes_digest(self):
        with self.assertRaisesRegex(ValueError, "media_sha256"):
            normalize(self.example(kind="image_text"))

    def test_benchmark_data_cannot_train(self):
        with self.assertRaisesRegex(ValueError, "evaluation-only"):
            self.splits([self.example(source="eval")])

    def test_license_approval_cannot_be_assumed(self):
        self.approvals["a"]["approved_for_training"] = False
        with self.assertRaisesRegex(ValueError, "approval"):
            self.splits([self.example()])

    def test_revision_mismatch_blocks(self):
        self.approvals["a"]["revision"] = "old"
        with self.assertRaisesRegex(ValueError, "approval"):
            self.splits([self.example()])

    def test_hf_preview_scrubs_raw_data(self):
        response = {"rows": [{"row": {"text": "sensitive raw sample text",
                                     "image": {"bytes": "private media bytes"},
                                     "score": 0.7}}]}
        summary = feature_summary(response)
        self.assertEqual(summary["row_count_in_preview"], 1)
        self.assertEqual([c["name"] for c in summary["columns"]], ["image", "score", "text"])
        self.assertNotIn("sensitive raw sample text", str(summary))
        self.assertNotIn("private media bytes", str(summary))


if __name__ == "__main__":
    unittest.main()
