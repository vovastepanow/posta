#!/usr/bin/env python3
"""Bounded pilot export of public text samples from HF Dataset Server.

Does not fetch Parquet shards, image/video/audio or dataset archives.
Requires approved exact revision; data stays inside ignored data/ directory.
"""
import argparse
import hashlib
import json
import os
import tempfile
import urllib.parse
from pathlib import Path

from probe import CATALOG, dataset_api_url, get_json, server_url

SUPPORTED = {
    "fineweb2-ru": ("rus_Cyrl", "ru"),
    "fineweb-en": ("default", "en"),
}


def approved(candidate, audit, approvals):
    audited = next((x for x in audit["datasets"] if x["id"] == candidate["id"]), None)
    approval = approvals.get(candidate["id"], {})
    if not audited or audited.get("metadata_status") != "ok":
        raise ValueError("No successful dataset metadata audit")
    revision = audited.get("revision")
    if (not revision or not approval.get("approved_for_training")
            or approval.get("revision") != revision
            or not str(approval.get("review_note", "")).strip()):
        raise ValueError("Need explicit source license approval for audited revision")
    return revision


def export(candidate, revision, config, language, out, count, stride):
    current = get_json(dataset_api_url(candidate["repo"])).get("sha")
    if current != revision:
        raise ValueError("Dataset HEAD moved since audit, rerun probe and review")
    out.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(prefix=".pmr-text-", suffix=".jsonl", dir=str(out.parent))
    accepted = 0
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            for n in range(count):
                offset = n * stride
                response = get_json(server_url(
                    "rows", dataset=candidate["repo"], config=config,
                    split="train", offset=offset, length=1))
                rows = response.get("rows", [])
                if not rows:
                    raise ValueError(f"No rows at offset {offset} (preview limit or dataset changed)")
                data = rows[0].get("row") or {}
                content = data.get("text")
                if not isinstance(content, str) or not content.strip():
                    raise ValueError(f"Missing text at offset {offset}")
                # For the first pilot, reject giant documents rather than silently truncate.
                if len(content) > 100_000:
                    raise ValueError(f"Document too long at offset {offset}")
                src_id = data.get("id")
                if not src_id:
                    src_id = hashlib.sha256(content.encode("utf-8")).hexdigest()
                row = {
                    "dataset_id": candidate["id"],
                    "record_id": str(src_id),
                    "kind": "text",
                    "language": language,
                    "text": content,
                    "group_id": str(src_id),
                }
                file.write(json.dumps(row, ensure_ascii=False) + "\n")
                accepted += 1
        # Verify head SHA after rows access to detect remote mutation during export.
        end_sha = get_json(dataset_api_url(candidate["repo"])).get("sha")
        if end_sha != revision:
            raise ValueError("Dataset revision changed during sampling; discard sample")
        os.replace(temp, out)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)
    return accepted


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--id", choices=sorted(SUPPORTED), required=True)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--audit", type=Path, default=Path("data/pmr/audit.json"))
    parser.add_argument("--approvals", type=Path, required=True)
    parser.add_argument("--out", type=Path, default=None)
    parser.add_argument("--count", type=int, default=20)
    parser.add_argument("--stride", type=int, default=997,
                        help="offset between successive rows; deterministic, NOT a uniform random sample")
    args = parser.parse_args()
    if not 1 <= args.count <= 200 or not 1 <= args.stride <= 10_000_000:
        parser.error("count must be 1..200; stride 1..10000000")
    catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
    audit = json.loads(args.audit.read_text(encoding="utf-8"))
    approvals = json.loads(args.approvals.read_text(encoding="utf-8"))
    candidate = next(x for x in catalog["datasets"] if x["id"] == args.id)
    config, language = SUPPORTED[args.id]
    revision = approved(candidate, audit, approvals)
    out = args.out or (Path("data/pmr") / f"{args.id}-sample.jsonl")
    print(json.dumps({"exported_rows": export(candidate, revision, config,
          language, out, args.count, args.stride),
          "dataset_id": args.id, "dataset_revision": revision, "output": str(out)}))


if __name__ == "__main__":
    main()
