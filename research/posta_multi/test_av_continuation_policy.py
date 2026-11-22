"""Offline tests for actual AV continuation scheduling and resume policy."""
import copy
import unittest

from av_continuation_policy import (
    pair_indices, select_training_pools, validated_progress,
)


def row(prefix, index):
    return {
        "kind": "video_text" if prefix == "v" else "speech_text",
        "record_id": f"{prefix}-{index}", "dataset_id": "local",
        "group_id": f"{prefix}-group-{index}", "language": "en",
        "text": f"{prefix} unique utterance number {index}",
        "duration_seconds": 4,
        "media": {"sha256": f"{index + (0 if prefix == 'v' else 100):064x}"},
    }


class FakeCorpus:
    def __init__(self):
        self.data = {
            ("video_text", "train"): [row("v", i) for i in range(7)],
            ("video_text", "validation"): [row("v", 99)],
            ("speech_text", "train"): [row("s", i) for i in range(3)],
            ("speech_text", "validation"): [row("s", 99)],
        }

    def rows(self, kind, split):
        return iter(self.data[(kind, split)])


class ContinuationPolicyTests(unittest.TestCase):
    def test_rotating_pairs_are_distinct_reproducible_and_cover_the_pool(self):
        for size in (2, 3, 4, 5, 16):
            count = (size + 1) // 2
            selections = [pair_indices(size, step) for step in range(1, count + 1)]
            self.assertEqual(len({i for pair in selections for i in pair}), size)
            self.assertTrue(all(len(set(pair)) == 2 for pair in selections))
            self.assertEqual(selections, [
                pair_indices(size, step) for step in range(1, count + 1)])

    def test_falls_back_to_actual_isolated_pool_size(self):
        pools, signatures = select_training_pools(FakeCorpus())
        self.assertEqual({kind: len(rows) for kind, rows in pools.items()},
                         {"video_text": 4, "speech_text": 2})
        self.assertEqual(set(signatures), {"video_text", "speech_text"})
        self.assertTrue(all(len(value) == 64 for value in signatures.values()))

    def test_resume_rejects_source_or_data_changes(self):
        state = {
            "format": "posta-av-235m-continuation-v1", "step": 8, "step_cap": 24,
            "source_checkpoint_sha256": "a" * 64,
            "manifest_sha256": "b" * 64,
            "tokenizer_sha256": "c" * 64,
            "pool_sha256": {"video_text": "d" * 64, "speech_text": "e" * 64},
            "model": {"some-weight": "placeholder"},
            "optimizer": {"step": 8},
            "quarantined_research_only": True, "private_local_only": True,
            "production_promotion_allowed": False,
        }
        opts = {"expected_step_cap": 24, "source_hash": "a" * 64,
                "manifest_hash": "b" * 64, "tokenizer_hash": "c" * 64,
                "pool_hashes": state["pool_sha256"]}
        self.assertEqual(validated_progress(state, **opts), 8)
        for name, invalid in (("step", 0), ("step", 25), ("step_cap", 25),
                              ("private_local_only", False),
                              ("production_promotion_allowed", True),
                              ("source_checkpoint_sha256", "f" * 64),
                              ("manifest_sha256", "f" * 64),
                              ("tokenizer_sha256", "f" * 64)):
            with self.subTest(name=name):
                changed = copy.deepcopy(state)
                changed[name] = invalid
                with self.assertRaisesRegex(ValueError, "incompatible"):
                    validated_progress(changed, **opts)
        changed = copy.deepcopy(state)
        changed["pool_sha256"]["speech_text"] = "f" * 64
        with self.assertRaisesRegex(ValueError, "incompatible"):
            validated_progress(changed, **opts)


if __name__ == "__main__":
    unittest.main()
