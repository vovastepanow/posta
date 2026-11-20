#!/usr/bin/env python3
"""Normalize a small, independently exported PMR JSONL pilot. No network or media downloads."""
import argparse
import hashlib
import json
import re
import sys
import unicodedata
from collections import Counter
from pathlib import Path

from probe import atomic_json

KINDS = {"text", "retrieval", "image_text", "ocr_text", "video_text", "meme_text", "speech_text"}
NEEDS_MEDIA = {"image_text", "ocr_text", "video_text", "meme_text", "speech_text"}


def clean(value):
    if not isinstance(value, str):
        return ""
    return re.sub(r"\s+", " ", unicodedata.normalize("NFKC", value)).strip()


def sha(value):
    return hashlib.sha256(value.encode("utf-8")).hexdigest()



def ocr_annotation_texts(raw, include_words=False):
    annotations = raw.get('ocr_annotations')
    if not isinstance(annotations, dict):
        return []
    categories = ['line_bboxes', 'para_bboxes']
    if include_words:
        categories.append('word_bboxes')
    values = []
    for category in categories:
        entries = annotations.get(category, [])
        if not isinstance(entries, list):
            continue
        for entry in entries:
            if isinstance(entry, dict) and clean(entry.get('text')):
                values.append((category, clean(entry['text'])))
    return values


def auxiliary_texts(raw):
    values = [raw.get('ocr'), raw.get('detailed_caption'), raw.get('subtitles')]
    values.extend(text for _, text in ocr_annotation_texts(raw))
    return [clean(value) for value in values if clean(value)]


def normalize(raw):
    dataset_id = clean(raw.get("dataset_id"))
    record_id = clean(raw.get("record_id"))
    kind = clean(raw.get("kind"))
    if not dataset_id or not record_id or kind not in KINDS:
        raise ValueError("dataset_id, record_id and valid kind are required")
    text = clean(raw.get("text"))
    positive = clean(raw.get("positive"))
    min_length = 1 if kind == "ocr_text" else 4
    if len(text) < min_length:
        raise ValueError(f"text/query must contain at least {min_length} characters")
    if kind == "retrieval" and len(positive) < 4:
        raise ValueError("retrieval requires positive passage")
    media_sha = clean(raw.get("media_sha256")).lower()
    if kind in NEEDS_MEDIA and not re.fullmatch(r"[0-9a-f]{64}", media_sha):
        raise ValueError("multimodal example requires media_sha256 supplied from verified media bytes")
    negatives = raw.get("negatives", [])
    if not isinstance(negatives, list):
        raise ValueError("negatives must be an array")
    negatives = [x for x in (clean(v) for v in negatives) if x and x != positive]
    auxiliary = raw.get('_auxiliary_texts')
    if auxiliary is None:
        auxiliary = auxiliary_texts(raw)
    if not isinstance(auxiliary, list):
        raise ValueError('auxiliary texts must be an array')
    auxiliary = [clean(value) for value in auxiliary if clean(value)]
    normalized = {
        "dataset_id": dataset_id,
        "record_id": record_id,
        "kind": kind,
        "language": clean(raw.get("language")) or "und",
        "text": text,
        "positive": positive if kind == "retrieval" else "",
        "negatives": list(dict.fromkeys(negatives)) if kind == "retrieval" else [],
        "media_sha256": media_sha,
        "group_id": clean(raw.get("group_id")) or f"{dataset_id}:{record_id}",
        "_auxiliary_texts": auxiliary,
    }
    # Group by content hashes; later a union-find joins exact matches across source groups.
    normalized["_fingerprint"] = sha(json.dumps([
        kind, text.casefold(), positive.casefold(),
        sorted(x.casefold() for x in normalized["negatives"]),
        [x.casefold() for x in auxiliary], media_sha
    ], ensure_ascii=False, separators=(",", ":")))
    return normalized


class UnionFind:
    def __init__(self, n):
        self.parent = list(range(n))

    def find(self, a):
        while self.parent[a] != a:
            self.parent[a] = self.parent[self.parent[a]]
            a = self.parent[a]
        return a

    def union(self, a, b):
        a, b = self.find(a), self.find(b)
        if a != b:
            self.parent[max(a, b)] = min(a, b)


def partition(rows):
    """Exact text/media and source groups cannot straddle output splits."""
    uf = UnionFind(len(rows))
    group_seen, fingerprint_seen, media_seen, source_seen, family_seen = {}, {}, {}, {}, {}
    content_seen = {}
    for i, row in enumerate(rows):
        for cache, key in (
            (group_seen, row["dataset_id"] + ":" + row["group_id"]),
            (fingerprint_seen, row["_fingerprint"]),
            (content_seen, sha(row["text"].casefold())),
            (source_seen, row.get("_source_url")),
            (family_seen, ("fineweb2:" + row["record_id"]) if row["dataset_id"] in {"fineweb2-ru", "fineweb2-hq-ru"} else None),
        ):
            if key is None:
                continue
            if key in cache:
                uf.union(i, cache[key])
            else:
                cache[key] = i
        # Any exact text shared across query, positive or negative roles stays together.
        for content in [row["positive"], *row.get("_auxiliary_texts", []), *row["negatives"]]:
            if not content:
                continue
            key = sha(content.casefold())
            if key in content_seen:
                uf.union(i, content_seen[key])
            else:
                content_seen[key] = i
        # Same media appearing with different captions still belongs to a single split.
        if row["media_sha256"]:
            key = row["media_sha256"]
            if key in media_seen:
                uf.union(i, media_seen[key])
            else:
                media_seen[key] = i
    clusters = {}
    for i in range(len(rows)):
        clusters.setdefault(uf.find(i), []).append(i)
    assignments = {}
    for indexes in clusters.values():
        # Stable across changes in input order, for a fixed set of rows.
        identity = min(rows[i]["_fingerprint"] for i in indexes)
        bucket = int(sha("posta-pmr-v0.1:" + identity)[:8], 16) % 100
        split = "train" if bucket < 90 else "validation" if bucket < 95 else "test"
        for i in indexes:
            assignments[i] = split
    return assignments


def check_approvals(rows, catalog, audit, approvals):
    catalog_by_id = {x["id"]: x for x in catalog["datasets"]}
    audit_by_id = {x["id"]: x for x in audit["datasets"]}
    for dataset_id in {row["dataset_id"] for row in rows}:
        candidate = catalog_by_id.get(dataset_id)
        checked = audit_by_id.get(dataset_id, {})
        approval = approvals.get(dataset_id, {})
        if not candidate or not candidate["training"]:
            raise ValueError(f"{dataset_id}: unknown or evaluation-only dataset")
        revision = checked.get("revision")
        if checked.get("metadata_status") != "ok" or not revision:
            raise ValueError(f"{dataset_id}: successful metadata probe with SHA required")
        if approval.get("approved_for_training") is not True or approval.get("revision") != revision:
            raise ValueError(f"{dataset_id}: explicit approval at the audited SHA required")
        if not clean(approval.get("review_note")):
            raise ValueError(f"{dataset_id}: review_note required in approvals")


def prepare(raw_rows, catalog, audit, approvals):
    rows = [normalize(x) for x in raw_rows]
    check_approvals(rows, catalog, audit, approvals)
    assignments = partition(rows)
    unique = set()
    splits = {"train": [], "validation": [], "test": []}
    duplicates = 0
    for i, row in enumerate(rows):
        if row["_fingerprint"] in unique:
            duplicates += 1
            continue
        unique.add(row["_fingerprint"])
        clean_row = {k: v for k, v in row.items() if not k.startswith("_")}
        splits[assignments[i]].append(clean_row)
    return splits, duplicates


def write_jsonl(path, rows):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as file:
        for row in rows:
            file.write(json.dumps(row, ensure_ascii=False, sort_keys=True) + "\n")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", type=Path, required=True, help="Pre-exported JSONL records")
    parser.add_argument("--catalog", type=Path, default=Path(__file__).with_name("catalog.json"))
    parser.add_argument("--audit", type=Path, default=Path("data/pmr/audit.json"))
    parser.add_argument("--approvals", type=Path, required=True, help="Human-reviewed license approvals")
    parser.add_argument("--out-dir", type=Path, default=Path("data/pmr/pilot"))
    args = parser.parse_args(argv)
    catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
    audit = json.loads(args.audit.read_text(encoding="utf-8"))
    approvals = json.loads(args.approvals.read_text(encoding="utf-8"))
    if not isinstance(approvals, dict):
        parser.error("approvals must be a JSON object keyed by dataset id")
    with args.input.open(encoding="utf-8") as file:
        raw = [json.loads(line) for line in file if line.strip()]
    splits, duplicates = prepare(raw, catalog, audit, approvals)
    for split, records in splits.items():
        write_jsonl(args.out_dir / (split + ".jsonl"), records)
    counts = Counter(row["dataset_id"] for records in splits.values() for row in records)
    atomic_json(args.out_dir / "manifest.json", {
        "version": "posta-pmr-v0.1",
        "counts": dict(counts),
        "splits": {k: len(v) for k, v in splits.items()},
        "exact_duplicates_removed": duplicates,
        "input_rows": len(raw),
        "dataset_revisions": {key: audit_row["revision"] for key, audit_row in (
            (row["id"], row) for row in audit["datasets"]
        ) if key in counts},
        "limitations": [
            "No image/video/audio bytes were decoded by this normalizer",
            "Near-duplicate media detection and benchmark contamination screening remain required",
            "Local test split is not the independent benchmark",
            "Only exact text and media digests grouped; pHash near duplicates and source-family overlap not handled",
        ],
    })
    print(json.dumps({"splits": {k: len(v) for k, v in splits.items()},
                      "removed_duplicates": duplicates}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
