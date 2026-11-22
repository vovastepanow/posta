"""Standard-library-only tests; intentionally runnable in Go CI without torch."""
import copy
import json
import tempfile
import unittest
from pathlib import Path

from av_checkpoint_compare import compare, _read


def sample_reports():
    pilot = {
        "format": "posta-av-235m-private-pilot-v1",
        "source_format": "posta-multi-235m-run001-retrieval-v1-best",
        "source_checkpoint_sha256": "a" * 64,
        "tokenizer_sha256": "b" * 64,
        "manifest_sha256": "c" * 64,
        "config": {"embed_dim": 512},
        "target_fps": 30,
        "video_time_encoding": "relative-centered-v1",
        "video_window_aggregation": "hierarchical-residual-v1",
        "quarantined_research_only": True,
        "private_local_only": True,
        "production_promotion_allowed": False,
        "quality_verified": False,
        "step": 1,
    }
    metrics = {
        "pairs": 4,
        "hit_at_1": .25,
        "mean_reciprocal_rank": .5,
        "mean_positive_cosine": .25,
        "mean_other_cosine": .1,
        "media_interexample_cosine": .99,
    }
    def report(fmt):
        return {
            "format": "posta-av-235m-heldout-pool-v1",
            "checkpoint_format": fmt,
            "manifest_sha256": "c" * 64,
            "target_fps": 30,
            "video_time_encoding": "relative-centered-v1",
            "video_window_aggregation": "hierarchical-residual-v1",
            "production_promotion_allowed": False,
            "modalities": {
                kind: {"selection_sha256": ch * 64,
                       "groups": 4 if kind == "video_text" else 1,
                       "frame_or_sample_counts": [120, 121, 122, 123],
                       "metrics": copy.deepcopy(metrics)}
                for kind, ch in (("video_text", "d"), ("speech_text", "e"))
            },
        }
    baseline = report(pilot["source_format"])
    after = report(pilot["format"])
    after["modalities"]["video_text"]["metrics"]["hit_at_1"] = .5
    return baseline, after, pilot


class CheckpointComparisonTests(unittest.TestCase):
    def test_valid_comparison_reports_deltas_without_promotion(self):
        baseline, after, pilot = sample_reports()
        result = compare(baseline, after, pilot)
        self.assertEqual(result["modalities"]["video_text"]["changes"]["hit_at_1"]["delta"], .25)
        self.assertEqual(result["modalities"]["speech_text"]["changes"]["hit_at_1"]["delta"], 0)
        self.assertFalse(result["quality_verified"])
        self.assertFalse(result["production_promotion_allowed"])
        self.assertEqual(result["modalities"]["speech_text"]["independent_groups_reported"], 1)

    def test_different_holdout_is_refused(self):
        for field, value in (("selection_sha256", "f" * 64),
                             ("groups", 2),
                             ("frame_or_sample_counts", [1, 2, 3, 4])):
            with self.subTest(field=field):
                baseline, after, pilot = sample_reports()
                after["modalities"]["video_text"][field] = value
                with self.assertRaisesRegex(ValueError, "held-out"):
                    compare(baseline, after, pilot)

    def test_preprocessing_and_checkpoint_mismatch_refused(self):
        for changed, value in (("video_time_encoding", "legacy"),
                               ("video_window_aggregation", "mean"),
                               ("target_fps", 1),
                               ("checkpoint_format", "unrelated")):
            with self.subTest(changed=changed):
                baseline, after, pilot = sample_reports()
                after[changed] = value
                with self.assertRaises(ValueError):
                    compare(baseline, after, pilot)

    def test_incomplete_training_and_nonfinite_metric_refused(self):
        baseline, after, pilot = sample_reports()
        pilot["production_promotion_allowed"] = True
        with self.assertRaises(ValueError):
            compare(baseline, after, pilot)
        baseline, after, pilot = sample_reports()
        after["modalities"]["video_text"]["metrics"]["mean_reciprocal_rank"] = float("nan")
        with self.assertRaisesRegex(ValueError, "invalid"):
            compare(baseline, after, pilot)

    def test_optional_video_order_metrics_and_version_mismatch(self):
        baseline, after, pilot = sample_reports()
        baseline["modalities"]["video_text"]["temporal_reversal_sensitivity"] = {
            "samples": 4, "mean_1_minus_cosine": .01,
            "min_1_minus_cosine": .001, "max_1_minus_cosine": .03,
        }
        before_only = compare(baseline, after, pilot)
        self.assertEqual(before_only["modalities"]["video_text"]["order_probe_comparison"],
                         "not_comparable_different_report_versions")
        after["modalities"]["video_text"]["temporal_reversal_sensitivity"] = {
            "samples": 4, "mean_1_minus_cosine": .02,
            "min_1_minus_cosine": .002, "max_1_minus_cosine": .06,
        }
        aligned = compare(baseline, after, pilot)
        self.assertAlmostEqual(aligned["modalities"]["video_text"]
                               ["order_probe_comparison"]["mean_1_minus_cosine"]["delta"], .01)
        after["modalities"]["video_text"]["temporal_reversal_sensitivity"]["samples"] = 3
        with self.assertRaisesRegex(ValueError, "pool sizes"):
            compare(baseline, after, pilot)

    def test_file_reader_blocks_missing_and_oversize(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "summary.json"
            with self.assertRaisesRegex(ValueError, "missing"):
                _read(path)
            path.write_text(json.dumps(sample_reports()[0]), encoding="utf-8")
            self.assertIn("modalities", _read(path))
            path.write_bytes(b" " * 1_000_001)
            with self.assertRaisesRegex(ValueError, "oversized"):
                _read(path)


if __name__ == "__main__":
    unittest.main()
