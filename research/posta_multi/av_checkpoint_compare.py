"""Fail-closed comparison of an AV research pilot with its frozen baseline.

Reads three local JSON summaries only: no checkpoint loading, CUDA, dataset
access, network, production files, or automatic model promotion.
"""
import argparse
import json
import math
from pathlib import Path

METRIC_NAMES = (
    "hit_at_1", "mean_reciprocal_rank", "mean_positive_cosine",
    "mean_other_cosine", "media_interexample_cosine",
)
KINDS = ("video_text", "speech_text")


def _require(condition, message):
    if not condition:
        raise ValueError(message)


def _number(value, name, lo, hi):
    _require(type(value) in (float, int) and math.isfinite(value) and
             lo - 1e-5 <= value <= hi + 1e-5,
             f"invalid {name}")
    return float(value)


def _read(path):
    path = Path(path)
    # These are local aggregate reports, never raw examples. Bound the input.
    _require(path.is_file() and 0 < path.stat().st_size <= 1_000_000,
             f"missing or oversized report: {path.name}")
    with path.open("r", encoding="utf-8") as stream:
        report = json.load(stream)
    _require(type(report) is dict, f"invalid report: {path.name}")
    return report


def compare(baseline, after, train):
    """Only compare the same held-out examples and the actual pilot provenance."""
    _require(train.get("format") == "posta-av-235m-private-pilot-v1",
             "unexpected training pilot format")
    _require(train.get("quarantined_research_only") is True and
             train.get("private_local_only") is True and
             train.get("production_promotion_allowed") is False and
             train.get("quality_verified") is False and
             train.get("step") == 1,
             "training summary cannot be used as a research-only one-step pilot")
    _require(type(train.get("source_checkpoint_sha256")) is str and
             len(train["source_checkpoint_sha256"]) == 64,
             "missing source checkpoint hash")
    _require(type(train.get("tokenizer_sha256")) is str and
             len(train["tokenizer_sha256"]) == 64,
             "missing tokenizer hash")
    _require(type(train.get("config")) is dict, "missing training configuration")

    for label, report in (("baseline", baseline), ("after", after)):
        _require(report.get("format") == "posta-av-235m-heldout-pool-v1" and
                 report.get("production_promotion_allowed") is False,
                 f"invalid {label} held-out report")
        _require(report.get("manifest_sha256") == train.get("manifest_sha256") and
                 type(report.get("manifest_sha256")) is str,
                 f"{label} dataset differs from pilot")
        _require(report.get("target_fps") == train.get("target_fps") and
                 report.get("video_time_encoding") == train.get("video_time_encoding") and
                 report.get("video_window_aggregation") == train.get("video_window_aggregation"),
                 f"{label} preprocessing does not match trained pilot")
        _require(type(report.get("modalities")) is dict and
                 set(report["modalities"]) == set(KINDS),
                 f"{label} must contain both held-out media modalities")

    _require(baseline.get("checkpoint_format") == train.get("source_format"),
             "baseline does not use the pilot source checkpoint format")
    _require(after.get("checkpoint_format") == train.get("format"),
             "after report is not from the pilot checkpoint")
    # Older reports lack file digests; compare their metrics but explicitly mark
    # the provenance as incomplete. Newly hashed reports must match exactly.
    for label, report in (("baseline", baseline), ("after", after)):
        token_hash = report.get("tokenizer_sha256")
        if token_hash is not None:
            _require(token_hash == train["tokenizer_sha256"],
                     f"{label} evaluation used a different tokenizer")
        checkpoint_hash = report.get("checkpoint_sha256")
        if checkpoint_hash is not None:
            _require(type(checkpoint_hash) is str and len(checkpoint_hash) == 64,
                     f"{label} checkpoint fingerprint is malformed")
    baseline_bound = baseline.get("checkpoint_sha256") is not None
    if baseline_bound:
        _require(baseline["checkpoint_sha256"] == train["source_checkpoint_sha256"],
                 "baseline was not evaluated against the actual pilot source weights")
    trained_hash = train.get("output_checkpoint_sha256")
    if trained_hash is not None:
        _require(type(trained_hash) is str and len(trained_hash) == 64,
                 "pilot output checkpoint fingerprint is malformed")
    after_bound = (trained_hash is not None and
                   after.get("checkpoint_sha256") is not None)
    if after_bound:
        _require(after["checkpoint_sha256"] == trained_hash,
                 "after evaluation was not run against the actual pilot output weights")
    output = {}
    for kind in KINDS:
        before = baseline["modalities"][kind]
        later = after["modalities"][kind]
        _require(type(before) is dict and type(later) is dict,
                 f"invalid {kind} records")
        for field in ("selection_sha256", "groups", "frame_or_sample_counts"):
            _require(before.get(field) == later.get(field),
                     f"{kind} held-out {field} differs; refusing comparison")
        _require(type(before.get("selection_sha256")) is str and
                 len(before["selection_sha256"]) == 64, f"{kind} missing selection hash")
        groups = before.get("groups")
        counts = before.get("frame_or_sample_counts")
        _require(type(groups) is int and groups >= 1 and
                 type(counts) is list and len(counts) >= 2 and
                 all(type(x) is int and x > 0 for x in counts),
                 f"{kind} invalid sample counts")
        _require(type(before.get("metrics")) is dict and
                 type(later.get("metrics")) is dict, f"{kind} missing metrics")
        a, b = before["metrics"], later["metrics"]
        _require(a.get("pairs") == b.get("pairs") == len(counts),
                 f"{kind} evaluation sizes differ")
        changes = {}
        for name in METRIC_NAMES:
            lower, upper = (0.0, 1.0) if name in ("hit_at_1", "mean_reciprocal_rank") else (-1.0, 1.0)
            x = _number(a.get(name), f"{kind} baseline {name}", lower, upper)
            y = _number(b.get(name), f"{kind} after {name}", lower, upper)
            changes[name] = {"baseline": x, "after": y, "delta": y - x}
        output[kind] = {
            "pairs": len(counts),
            "independent_groups_reported": groups,
            "selection_sha256": before["selection_sha256"],
            "changes": changes,
        }
        if kind == "video_text":
            original_order = before.get("temporal_reversal_sensitivity")
            trained_order = later.get("temporal_reversal_sensitivity")
            if (original_order is None) != (trained_order is None):
                output[kind]["order_probe_comparison"] = "not_comparable_different_report_versions"
            elif original_order is not None:
                _require(type(original_order) is dict and type(trained_order) is dict,
                         "invalid video reversal diagnostics")
                _require(original_order.get("samples") == trained_order.get("samples") == len(counts),
                         "video reversal diagnostic pool sizes differ")
                order_changes = {}
                for key in ("mean_1_minus_cosine", "min_1_minus_cosine", "max_1_minus_cosine"):
                    initial = _number(original_order.get(key), "baseline " + key, 0, 2)
                    final = _number(trained_order.get(key), "after " + key, 0, 2)
                    order_changes[key] = {"baseline": initial, "after": final,
                                          "delta": final - initial}
                output[kind]["order_probe_comparison"] = order_changes
    return {
        "format": "posta-av-private-baseline-comparison-v1",
        "source_checkpoint_sha256": train["source_checkpoint_sha256"],
        "tokenizer_sha256": train["tokenizer_sha256"],
        "manifest_sha256": train["manifest_sha256"],
        "video_time_encoding": train["video_time_encoding"],
        "video_window_aggregation": train["video_window_aggregation"],
        "modalities": output,
        "checkpoint_hashes_verified": {
            "baseline": baseline_bound,
            "after": after_bound,
        },
        "quality_verified": False,
        "production_promotion_allowed": False,
        "decision": "exploratory_only_no_automatic_training_or_promotion",
        "limitations": [
            "one training step and tiny same-dataset pools",
            "paired candidate captions do not rule out other relevant results",
            "speech validation may contain a single speaker group",
            "single held-out pool does not establish generalization",
            "cosine geometry and metric changes are not proof of video or audio understanding",
        ],
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", required=True)
    parser.add_argument("--after", required=True)
    parser.add_argument("--train", required=True)
    args = parser.parse_args()
    print(json.dumps(compare(_read(args.baseline), _read(args.after), _read(args.train)),
                     ensure_ascii=False, indent=2, allow_nan=False))


if __name__ == "__main__":
    main()
