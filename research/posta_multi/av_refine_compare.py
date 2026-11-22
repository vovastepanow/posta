"""Compare the exact same frozen, split-isolated AV pool before/after refinement.

Reads aggregate reports only. Never infers relevance from unlabeled posts,
selects a model, writes weights, or promotes a checkpoint to production.
"""
import argparse
import json
import math
from pathlib import Path

KINDS = ("video_text", "speech_text")
METRICS = ("hit_at_1", "mean_reciprocal_rank", "mean_positive_cosine",
           "mean_other_cosine", "media_interexample_cosine")


def _finite(value):
    if type(value) not in (int, float) or not math.isfinite(value):
        raise ValueError("missing or nonfinite AV comparison metric")
    return float(value)


def compare(before, after, preflight):
    if (before.get("format") != "posta-av-235m-heldout-pool-v1" or
            after.get("format") != "posta-av-235m-heldout-pool-v1" or
            preflight.get("format") != "posta-av-235m-refine-preflight-v1" or
            before.get("checkpoint_format") != "posta-av-235m-continuation-v1" or
            after.get("checkpoint_format") != "posta-av-235m-geometry-refinement-v1"):
        raise ValueError("checkpoint formats differ from controlled refinement")
    for report in (before, after):
        if (report.get("manifest_sha256") != preflight.get("manifest_sha256") or
                report.get("production_promotion_allowed") is not False or
                report.get("video_time_encoding") != "relative-centered-v1" or
                report.get("video_window_aggregation") != "hierarchical-residual-v1" or
                type(report.get("checkpoint_sha256")) is not str or
                len(report["checkpoint_sha256"]) != 64 or
                type(report.get("tokenizer_sha256")) is not str or
                len(report["tokenizer_sha256"]) != 64):
            raise ValueError("invalid held-out provenance")
    if (before["tokenizer_sha256"] != after["tokenizer_sha256"] or
            before["checkpoint_sha256"] == after["checkpoint_sha256"]):
        raise ValueError("identical checkpoints or mismatched tokenizers")
    output = {}
    for kind in KINDS:
        a = before.get("modalities", {}).get(kind, {})
        b = after.get("modalities", {}).get(kind, {})
        expected = preflight.get("heldout", {}).get(kind, {})
        if (a.get("selection_sha256") != expected.get("selection_sha256") or
                b.get("selection_sha256") != expected.get("selection_sha256") or
                a.get("frame_or_sample_counts") != b.get("frame_or_sample_counts") or
                a.get("groups") != b.get("groups") or
                a.get("groups") != expected.get("groups") or
                type(expected.get("records")) is not int or
                expected["records"] < 8):
            raise ValueError(f"mismatched held-out pool for {kind}")
        a_metrics = a.get("metrics", {})
        b_metrics = b.get("metrics", {})
        if (a_metrics.get("pairs") != b_metrics.get("pairs") or
                a_metrics.get("pairs") != expected["records"]):
            raise ValueError("unequal candidate-pool size")
        differences = {}
        for metric in METRICS:
            x, y = _finite(a_metrics.get(metric)), _finite(b_metrics.get(metric))
            differences[metric] = {"before": x, "after": y, "delta": y - x}
        ref_a, ref_b = a.get("teacher_media_geometry", {}), b.get("teacher_media_geometry", {})
        if (not ref_a or not ref_b or
                ref_a.get("samples") != ref_b.get("samples") or
                ref_a.get("samples") != expected["records"]):
            raise ValueError("missing reference geometry")
        geometry = {}
        for metric in ("text_interexample_cosine",
                       "text_interexample_cosine_std",
                       "media_interexample_cosine",
                       "media_interexample_cosine_std",
                       "mean_absolute_geometry_error"):
            x, y = _finite(ref_a.get(metric)), _finite(ref_b.get(metric))
            if metric.startswith("text_") and abs(x-y) > 1e-5:
                raise ValueError("frozen text reference changed")
            geometry[metric] = {"before": x, "after": y, "delta": y-x}
        output[kind] = {
            "samples": expected["records"],
            "independent_groups": expected["groups"],
            "metrics": differences,
            "teacher_media_geometry": geometry,
        }
    return {
        "format": "posta-av-235m-refinement-comparison-v1",
        "source_checkpoint_sha256": before["checkpoint_sha256"],
        "trained_checkpoint_sha256": after["checkpoint_sha256"],
        "manifest_sha256": preflight["manifest_sha256"],
        "modalities": output,
        "quality_verified": False,
        "production_promotion_allowed": False,
        "decision": "exploratory_only_insufficient_to_promote_without_relevance_labels",
        "limitations": [
            "Only same-dataset heldout candidates, not broad social media search",
            "Unjudged alternate posts could also be relevant",
            "Teacher similarity is not ground-truth semantic relevance",
            "No independent general-domain retrieval benchmark",
        ],
    }


def _read(path):
    path = Path(path)
    if not path.is_file() or not 0 < path.stat().st_size < 1000000:
        raise ValueError("missing or oversized comparison report")
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError("comparison input must be a JSON object")
    return value


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for argument in ("before", "after", "preflight"):
        parser.add_argument("--" + argument, required=True)
    args = parser.parse_args()
    print(json.dumps(compare(_read(args.before), _read(args.after),
                             _read(args.preflight)), ensure_ascii=False,
                     allow_nan=False, indent=2), flush=True)
