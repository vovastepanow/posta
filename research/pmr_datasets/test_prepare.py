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

    def test_query_and_positive_in_different_rows_share_split(self):
        from prepare import partition
        rows = [
            normalize(self.example(record="1", kind="retrieval", text="первый запрос",
                                   positive="общий документ")),
            normalize(self.example(record="2", kind="retrieval", text="общий документ",
                                   positive="второй документ")),
        ]
        assignments = partition(rows)
        self.assertEqual(assignments[0], assignments[1])

    def test_source_url_groups_different_text_across_sources(self):
        from prepare import partition
        first = normalize(self.example(source='a', record='1', text='первая версия текста'))
        rows = None
        for i in range(1000):
            second = normalize(self.example(source='b', record=str(i), text=f'другая версия текста {i}'))
            candidate = [first, second]
            before = partition(candidate)
            if before[0] != before[1]:
                rows = candidate
                break
        self.assertIsNotNone(rows)
        for row in rows:
            row['_source_url'] = 'https://example.test/article'
        splits = partition(rows)
        self.assertEqual(splits[0], splits[1])

    def test_auxiliary_ocr_text_groups_across_roles(self):
        from prepare import partition
        first = normalize(self.example(source='a', record='1', kind='meme_text',
                                       text='пояснение картинки', ocr='общий текст внутри',
                                       media_sha256=media_hash('meme')))
        rows = None
        for i in range(1000):
            other = normalize(self.example(source='b', record=str(i), kind='retrieval',
                                          text='общий текст внутри', positive=f'документ номер {i}'))
            # A copy with auxiliary content removed demonstrates the meaningful comparison.
            without = dict(first, _auxiliary_texts=[])
            before = partition([without, other])
            if before[0] != before[1]:
                rows = [first, other]
                break
        self.assertIsNotNone(rows)
        splits = partition(rows)
        self.assertEqual(splits[0], splits[1])

    def test_fineweb2_family_id_groups_different_versions(self):
        from prepare import partition
        first = normalize(self.example(source='fineweb2-ru', record='same-id', text='первая версия текста'))
        rows = None
        for i in range(1000):
            second = normalize(self.example(source='unrelated', record='same-id', text=f'другая версия {i}'))
            if partition([first, second])[0] != partition([first, second])[1]:
                second['dataset_id'] = 'fineweb2-hq-ru'
                rows = [first, second]
                break
        self.assertIsNotNone(rows)
        assignments = partition(rows)
        self.assertEqual(assignments[0], assignments[1])

    def test_ocr_annotation_line_groups_with_query(self):
        from prepare import partition
        first = normalize(self.example(source='a', record='1', kind='ocr_text',
                                       text='полная OCR надпись', media_sha256=media_hash('ocr'),
                                       ocr_annotations={'line_bboxes':[{'text':'общая строка'}]}))
        rows = None
        for i in range(1000):
            other = normalize(self.example(source='b', record=str(i), kind='retrieval',
                                           text='общая строка', positive=f'документ номер {i}'))
            before = partition([dict(first, _auxiliary_texts=[]), other])
            if before[0] != before[1]:
                rows = [first, other]
                break
        self.assertIsNotNone(rows)
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

    def test_short_ocr_label_is_valid(self):
        row = normalize(self.example(kind="ocr_text", text="я", media_sha256=media_hash("image")))
        self.assertEqual(row["text"], "я")

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

    def test_string_approval_does_not_grant_permission(self):
        self.approvals["a"]["approved_for_training"] = "false"
        with self.assertRaisesRegex(ValueError, "approval"):
            self.splits([self.example()])

    def test_negative_sharing_a_positive_stays_in_same_split(self):
        from prepare import partition
        rows = [normalize(self.example(record="1", kind="retrieval", text="first query",
                                       positive="shared passage")),
                normalize(self.example(record="2", kind="retrieval", text="second query",
                                       positive="other passage", negatives=["shared passage"]))]
        assignments = partition(rows)
        self.assertEqual(assignments[0], assignments[1])

    def test_distinct_negatives_are_not_deduplicated(self):
        rows = [self.example(record=str(i), kind="retrieval", text="same query",
                             positive="same document", negatives=[negative])
                for i, negative in enumerate(("negative one", "negative two"))]
        splits, duplicates = self.splits(rows)
        self.assertEqual(duplicates, 0)
        self.assertEqual(sum(map(len, splits.values())), 2)

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
