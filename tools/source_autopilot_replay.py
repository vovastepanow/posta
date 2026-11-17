#!/usr/bin/env python3
"""Summarize blind annotations and recorded baseline/auto decisions.

Input is the sample produced by source_autopilot_blind_sample.py. Each item
may later receive annotation={"relevant": bool, "duplicate": bool,
"advertising": bool} and decisions={"baseline": bool, "auto": bool}.
Missing values stay unknown and are never counted as failures or successes.
"""
import json
import sys
from collections import defaultdict
from pathlib import Path


def rate(numerator, denominator):
    return numerator / denominator if denominator else None


def summarize(samples):
    groups = defaultdict(list)
    for sample in samples:
        groups[sample["profile"]].append(sample)
    result = {}
    for profile, items in sorted(groups.items()):
        by_system = {}
        for system in ("baseline", "auto"):
            decided = [item for item in items if isinstance((item.get("decisions") or {}).get(system), bool)]
            admitted = [item for item in decided if item["decisions"][system]]
            relevance = [item for item in admitted if isinstance((item.get("annotation") or {}).get("relevant"), bool)]
            duplicates = [item for item in admitted if isinstance((item.get("annotation") or {}).get("duplicate"), bool)]
            advertising = [item for item in admitted if isinstance((item.get("annotation") or {}).get("advertising"), bool)]
            by_system[system] = {
                "decisions": len(decided), "admitted": len(admitted),
                "relevance_n": len(relevance), "relevance_rate": rate(sum(item["annotation"]["relevant"] for item in relevance), len(relevance)),
                "duplicate_n": len(duplicates), "duplicate_rate": rate(sum(item["annotation"]["duplicate"] for item in duplicates), len(duplicates)),
                "advertising_n": len(advertising), "advertising_rate": rate(sum(item["annotation"]["advertising"] for item in advertising), len(advertising)),
            }
        result[profile] = {"samples": len(items), "annotated": sum(bool(item.get("annotation")) for item in items), "systems": by_system}
    return result


def main():
    path = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parent / "fixtures/source_autopilot_blind_sample.json"
    data = json.loads(path.read_text(encoding="utf-8"))
    print(json.dumps({"profiles": summarize(data["samples"]), "note": "Unknown labels and unrecorded decisions are excluded from denominators."}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
