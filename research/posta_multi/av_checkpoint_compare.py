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
    return {
        "format": "posta-av-private-baseline-comparison-v1",
        "source_checkpoint_sha256": train["source_checkpoint_sha256"],
        "tokenizer_sha256": train["tokenizer_sha256"],
        "manifest_sha256": train["manifest_sha256"],
        "video_time_encoding": train["video_time_encoding"],
        "video_window_aggregation": train["video_window_aggregation"],
        "modalities": output,
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
