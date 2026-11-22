"""Offline gating, source provenance and measured result tests for AV stage 2."""
import copy
import unittest
from dataclasses import asdict

from av_235m_refine import FORMAT, validated_refinement, verified_source
from av_refine_compare import compare
from spec import Config


class RefineTests(unittest.TestCase):
    def setUp(self):
        self.sha = "a" * 64
        self.manifest = "b" * 64
        self.tokenizer = "c" * 64
        self.source = {
            "format": "posta-av-235m-continuation-v1",
            "quarantined_research_only": True, "private_local_only": True,
            "production_promotion_allowed": False,
            "step": 24, "step_cap": 24,
            "manifest_sha256": self.manifest,
            "tokenizer_sha256": self.tokenizer,
            "config": asdict(Config()), "model": {}, "optimizer": {},
        }
        self.preflight = {
            "format": "posta-av-235m-refine-preflight-v1",
            "manifest_sha256": self.manifest,
            "heldout": {
                kind: {"records": 8, "groups": 8, "selection_sha256": "e" * 64}
                for kind in ("video_text", "speech_text")
            },
        }
        base = {
            "format": "posta-av-235m-heldout-pool-v1",
            "checkpoint_format": "posta-av-235m-continuation-v1",
            "checkpoint_sha256": self.sha,
            "tokenizer_sha256": self.tokenizer,
            "manifest_sha256": self.manifest,
            "video_time_encoding": "relative-centered-v1",
            "video_window_aggregation": "hierarchical-residual-v1",
            "production_promotion_allowed": False,
            "modalities": {},
        }
        for kind in ("video_text", "speech_text"):
            base["modalities"][kind] = {
                "groups": 8, "selection_sha256": "e" * 64,
                "frame_or_sample_counts": [64] * 8,
                "metrics": {"pairs": 8, "hit_at_1": .25,
                            "mean_reciprocal_rank": .55,
                            "mean_positive_cosine": .5,
                            "mean_other_cosine": .4,
                            "media_interexample_cosine": .99},
                "teacher_media_geometry": {
                    "samples": 8, "text_interexample_cosine": .89,
                    "text_interexample_cosine_std": .05,
                    "media_interexample_cosine": .99,
                    "media_interexample_cosine_std": .001,
                    "mean_absolute_geometry_error": .1,
                },
            }
        self.before = base
        self.after = copy.deepcopy(base)
        self.after["checkpoint_format"] = FORMAT
        self.after["checkpoint_sha256"] = "d" * 64
        for kind in ("video_text", "speech_text"):
            self.after["modalities"][kind]["metrics"]["mean_reciprocal_rank"] = .6

    def test_validated_source_requires_real_frozen_reference(self):
        config = verified_source(self.source, self.before,
                                 manifest_sha=self.manifest,
                                 tokenizer_sha=self.tokenizer,
                                 source_sha=self.sha)
        self.assertEqual(config, Config())
        bad = copy.deepcopy(self.before)
        bad["modalities"]["video_text"]["teacher_media_geometry"][
            "text_interexample_cosine"] = .997
        with self.assertRaisesRegex(ValueError, "too collapsed"):
            verified_source(self.source, bad, manifest_sha=self.manifest,
                            tokenizer_sha=self.tokenizer, source_sha=self.sha)
        stale = copy.deepcopy(self.source)
        stale["step"] = 22
        with self.assertRaisesRegex(ValueError, "24-step"):
            verified_source(stale, self.before, manifest_sha=self.manifest,
                            tokenizer_sha=self.tokenizer, source_sha=self.sha)

    def test_report_is_exact_pool_and_does_not_promote(self):
        result = compare(self.before, self.after, self.preflight)
        self.assertAlmostEqual(
            result["modalities"]["video_text"]["metrics"][
                "mean_reciprocal_rank"]["delta"], .05)
        self.assertFalse(result["production_promotion_allowed"])
        mismatch = copy.deepcopy(self.after)
        mismatch["modalities"]["video_text"]["selection_sha256"] = "f" * 64
        with self.assertRaisesRegex(ValueError, "mismatched held-out"):
            compare(self.before, mismatch, self.preflight)
        drift = copy.deepcopy(self.after)
        drift["modalities"]["speech_text"]["teacher_media_geometry"][
            "text_interexample_cosine"] = .5
        with self.assertRaisesRegex(ValueError, "frozen text"):
            compare(self.before, drift, self.preflight)

    def test_resume_rejects_mismatched_objective(self):
        settings = {"source_checkpoint_sha256": self.sha,
                    "objective": "positive-alignment-plus-teacher-chord-geometry-v1",
                    "step_cap": 8}
        state = {
            "format": FORMAT, "settings": settings, "step": 2,
            "model": {}, "optimizer": {}, "quarantined_research_only": True,
            "private_local_only": True, "production_promotion_allowed": False,
        }
        self.assertEqual(validated_refinement(state, settings), 2)
        other = dict(settings, step_cap=24)
        with self.assertRaisesRegex(ValueError, "cannot be resumed"):
            validated_refinement(state, other)


if __name__ == "__main__":
    unittest.main()
