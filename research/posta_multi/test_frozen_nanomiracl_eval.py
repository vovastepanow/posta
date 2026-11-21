"""Offline integrity and ranking checks for the frozen Run001 NanoMIRACL evaluator."""
import hashlib
import json
import math
import tempfile
import unittest
from pathlib import Path

from frozen_nanomiracl_eval import (BM25_FORMAT, FROZEN_FORMAT,
                                    evaluate_groups, load_frozen, logical_sha, score_ranking,
                                    summarize_scores)
from nanomiracl_benchmark import SOURCE


def fixture():
    groups = {}
    for lang in ("ru", "en"):
        queries = {f"q{i:03}": f"query {i} {lang}" for i in range(200)}
        docs = {f"d{i:03}": f"document {i} {lang}" for i in range(400)}
        qrels = {f"q{i:03}": [f"d{i:03}"] for i in range(200)}
        groups[lang] = {"queries": queries, "corpus": docs, "qrels": qrels}
    return groups


def write_fixture(folder, groups):
    sha = logical_sha(groups)
    frozen = folder / "snapshot.json"
    baseline = folder / "baseline.json"
    payload = {
        "format": FROZEN_FORMAT, "source": SOURCE,
        "source_revision_observed": "c" * 40,
        "source_content_sha256": sha, "languages": groups,
    }
    data = json.dumps(payload, ensure_ascii=False, sort_keys=True,
                      separators=(",", ":")).encode()
    frozen.write_bytes(data)
    baseline.write_text(json.dumps({
        "format": BM25_FORMAT, "evaluation_only": True,
        "source_content_sha256": sha,
        "source_revision_observed": "c" * 40,
        "frozen_file_sha256": hashlib.sha256(data).hexdigest(),
        "candidate_corpora": {
            lang: {"queries": 200, "documents": 400, "qrels": 200}
            for lang in ("ru", "en")},
    }), encoding="utf-8")
    return frozen, baseline


class FrozenNanoEvalTests(unittest.TestCase):
    def test_snapshot_roundtrip_and_file_tampering_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            frozen, baseline = write_fixture(folder, fixture())
            groups, record, file_sha = load_frozen(frozen, baseline)
            self.assertEqual(set(groups), {"ru", "en"})
            self.assertEqual(file_sha, record["frozen_file_sha256"])
            frozen.write_bytes(frozen.read_bytes() + b" ")
            with self.assertRaisesRegex(ValueError, "SHA-pinned"):
                load_frozen(frozen, baseline)

    def test_baseline_requires_frozen_file_digest(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            frozen, baseline = write_fixture(folder, fixture())
            record = json.loads(baseline.read_text())
            del record["frozen_file_sha256"]
            baseline.write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, "SHA-pinned"):
                load_frozen(frozen, baseline)

    def test_invalid_qrel_rejected_even_when_sha_matches(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            groups = fixture()
            groups["ru"]["qrels"]["q000"] = ["not-a-document"]
            frozen, baseline = write_fixture(folder, groups)
            with self.assertRaisesRegex(ValueError, "relevance identifiers"):
                load_frozen(frozen, baseline)

    def test_wrong_source_revision_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            frozen, baseline = write_fixture(folder, fixture())
            record = json.loads(baseline.read_text())
            record["source_revision_observed"] = "e" * 40
            baseline.write_text(json.dumps(record))
            with self.assertRaisesRegex(ValueError, "revision"):
                load_frozen(frozen, baseline)

    def test_true_recall_and_hit_at_k_are_not_conflated(self):
        row = score_ranking(["a", "b", "c", "d", "e", "f"],
                            [0.8, 0.7, 0.6, 0.5, 0.4, 0.3],
                            {"a", "f"})
        self.assertEqual(row["hit_5"], 1.0)
        self.assertEqual(row["positive_recall_5"], 0.5)
        self.assertEqual(row["positive_recall_10"], 1.0)
        self.assertEqual(row["mrr"], 1.0)
        self.assertTrue(0 < row["ndcg_10"] < 1)
        self.assertEqual(summarize_scores([row, row])["queries"], 2)

    def test_ties_have_stable_document_id_order(self):
        row = score_ranking(["z", "a", "m"], [0.1, 0.1, 0.1], {"a"})
        self.assertEqual(row["hit_1"], 1)
        self.assertEqual(row["mrr"], 1)
        with self.assertRaisesRegex(ValueError, "non-finite"):
            score_ranking(["a", "b"], [0.1, math.nan], {"a"})
        with self.assertRaisesRegex(ValueError, "absent"):
            score_ranking(["a", "b"], [0.1, 0.2], {"missing"})


    def test_full_candidate_evaluation_with_stub_512d_embeddings(self):
        import torch
        groups = {}
        for lang in ("ru", "en"):
            groups[lang] = {
                "queries": {"q1": "red", "q2": "blue"},
                "corpus": {"a": "red", "b": "blue", "c": "irrelevant"},
                "qrels": {"q1": ["a"], "q2": ["b"]},
            }

        def embed(text):
            vector = torch.zeros(512)
            vector[0 if text == "red" else 1 if text == "blue" else 2] = 1
            return vector

        results = evaluate_groups(groups, embed)
        self.assertEqual(results["overall"]["queries"], 4)
        self.assertEqual(results["overall"]["hit_1"], 1)
        self.assertEqual(results["overall"]["mrr"], 1)
        self.assertEqual(results["languages"]["ru"]["positive_recall_5"], 1)


if __name__ == "__main__":
    unittest.main()
