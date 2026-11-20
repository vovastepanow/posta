#!/usr/bin/env python3
"""Read-only metadata/schema audit of Hugging Face datasets; never downloads source shards."""
import argparse
import json
import os
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

CATALOG = Path(__file__).with_name("catalog.json")
MAX_HTTP_BYTES = 4 * 1024 * 1024


def get_json(url, timeout=20):
    request = urllib.request.Request(
        url, headers={"User-Agent": "Posta-PMR-dataset-audit/0.1", "Accept": "application/json"}
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read(MAX_HTTP_BYTES + 1)
        if len(raw) > MAX_HTTP_BYTES:
            raise ValueError("Response exceeds 4 MiB; use a narrower query")
        return json.loads(raw)


def dataset_api_url(repo):
    return "https://huggingface.co/api/datasets/" + urllib.parse.quote(repo, safe="/")


def server_url(endpoint, **params):
    return "https://datasets-server.huggingface.co/" + endpoint + "?" + urllib.parse.urlencode(params)


def choose_split(splits, item):
    preferred_config = item.get("preferred_config")
    preferred_split = item.get("preferred_split", "train")
    rows = [row for row in splits if row.get("dataset") == item["repo"]]
    if preferred_config:
        rows = [row for row in rows if row.get("config") == preferred_config]
    if not rows:
        return None
    for row in rows:
        if row.get("split") == preferred_split:
            return {"config": row["config"], "split": row["split"]}
    # Do not silently fall back to validation/test or to another language/config.
    return None


def feature_summary(first_rows):
    # Never persist raw text, images, links or other user-generated content in the audit.
    rows = first_rows.get("rows") or []
    if not rows:
        return {"row_count_in_preview": 0, "columns": []}
    first = rows[0].get("row") or {}
    if not isinstance(first, dict):
        return {"row_count_in_preview": len(rows), "columns": [], "warning": "unexpected row type"}
    return {
        "row_count_in_preview": len(rows),
        "columns": [
            {"name": key, "python_type": type(value).__name__}
            for key, value in sorted(first.items())
        ],
    }


def audit_one(item, inspect_rows=False):
    report = {
        "id": item["id"],
        "repo": item["repo"],
        "use": item["use"],
        "priority": item["priority"],
        "training": item["training"],
        "license_claim_from_catalog": item["license_claim"],
        "license_review": item["license_review"],
        "metadata_status": "not_checked",
        "schema_status": "not_checked",
        "media_bytes_verified": False,
    }
    try:
        meta = get_json(dataset_api_url(item["repo"]))
        card = meta.get("cardData") or {}
        siblings = meta.get("siblings") or []
        report.update({
            "metadata_status": "ok",
            "revision": meta.get("sha"),
            "license_from_hf_card": card.get("license"),
            "gated": meta.get("gated"),
            "private": meta.get("private"),
            "sibling_count": len(siblings),
            "example_filenames": [f.get("rfilename") for f in siblings[:12]],
            "review_required": True,
        })
    except (ValueError, OSError, urllib.error.URLError) as exc:
        report["metadata_status"] = "error"
        report["metadata_error"] = str(exc)[:300]
        return report

    if not inspect_rows:
        report["schema_status"] = "skipped; pass --rows to inspect remote preview"
        return report

    try:
        splits = get_json(server_url("splits", dataset=item["repo"])).get("splits", [])
        selected = choose_split(splits, item)
        if not selected:
            report["schema_status"] = "unavailable"
            report["schema_error"] = "preferred training split/config missing or dataset-server unsupported"
            return report
        report["preview_selection"] = selected
        response = get_json(server_url("first-rows", dataset=item["repo"], **selected))
        report["preview"] = feature_summary(response)
        report["schema_status"] = "ok"
    except (ValueError, OSError, urllib.error.URLError) as exc:
        report["schema_status"] = "error"
        report["schema_error"] = str(exc)[:300]
    return report


def atomic_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(prefix=".pmr-audit-", suffix=".json", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as file:
            json.dump(value, file, ensure_ascii=False, indent=2, sort_keys=True)
            file.write("\n")
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--out", type=Path, default=Path("data/pmr/audit.json"))
    parser.add_argument("--ids", nargs="*", help="Dataset IDs to probe; default: all")
    parser.add_argument("--rows", action="store_true", help="Inspect preview columns, not raw data")
    parser.add_argument("--strict", action="store_true", help="Fail on unavailable metadata/schema")
    args = parser.parse_args(argv)
    catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
    selected = [x for x in catalog["datasets"] if not args.ids or x["id"] in args.ids]
    if args.ids and set(args.ids) != {x["id"] for x in selected}:
        parser.error("Unknown id in --ids; see catalog.json")
    results = []
    for item in selected:
        result = audit_one(item, args.rows)
        results.append(result)
        print(f'{result["id"]}: metadata={result["metadata_status"]}, schema={result["schema_status"]}', file=sys.stderr)
    output = {
        "catalog_version": catalog["version"],
        "checked_at_utc": datetime.now(timezone.utc).isoformat(),
        "important": "Preview/schema metadata is not proof that full source media is downloadable or properly licensed",
        "datasets": results,
    }
    atomic_json(args.out, output)
    print(str(args.out))
    bad = any(x["metadata_status"] != "ok" or (
        args.rows and x["schema_status"] != "ok"
    ) for x in results)
    return int(args.strict and bad)


if __name__ == "__main__":
    sys.exit(main())
