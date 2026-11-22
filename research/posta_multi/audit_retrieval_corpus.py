#!/usr/bin/env python3
"""Audit real Posta retrieval snapshot before multimodal benchmarking.

No external libraries, network, model loading, or database writes. Reports only
aggregate statistics and top source counts; never prints raw post contents.
"""
from __future__ import annotations

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import re

from benchmark_embed import caption, local_media, read_jsonl

PLACEHOLDER = re.compile(
    r"^\s*(?:пост без текста|без текста|нет текста|no text|no caption|untitled)\s*[.!…]*\s*$",
    re.IGNORECASE,
)
TOKEN = re.compile(r"[^\W_]+", re.UNICODE)


def media_references(data: dict) -> tuple[bool, bool]:
    entries = [data]
    if isinstance(data.get("gallery"), list):
        entries.extend(v for v in data["gallery"] if isinstance(v, dict))
    return (
        any(isinstance(v.get("image_local"), str) and v["image_local"].strip() for v in entries),
        any(isinstance(v.get("video_local"), str) and v["video_local"].strip() for v in entries),
    )


def audit(rows: list[dict], media_dir: Path, media_url: str) -> dict:
    sources, platforms, exact_texts, descriptions = Counter(), Counter(), Counter(), Counter()
    image_refs = video_refs = image_ok = video_ok = 0
    short = placeholders = no_text = 0
    data_keys = Counter()
    for record in rows:
        data = record.get("data")
        if not isinstance(data, dict):
            raise ValueError("Invalid snapshot row: data must be an object")
        sources[record.get("source_key", "(unknown)")] += 1
        platforms[record.get("platform", "(unknown)")] += 1
        text = caption(data).strip()
        tokens = TOKEN.findall(text.casefold())
        if not text:
            no_text += 1
        elif PLACEHOLDER.fullmatch(text):
            placeholders += 1
        elif len(tokens) < 6:
            short += 1
        if text:
            exact_texts[" ".join(text.casefold().split())] += 1
        description = data.get("description")
        if isinstance(description, str) and len(description.strip()) > 30:
            descriptions[description.strip()] += 1
        for key in data:
            data_keys[key] += 1
        has_image, has_video = media_references(data)
        image_refs += bool(has_image)
        video_refs += bool(has_video)
        image, video = local_media(data, media_dir, media_url)
        image_ok += bool(image)
        video_ok += bool(video)

    n = len(rows)
    duplicate_texts = sum(count - 1 for count in exact_texts.values() if count > 1)
    repeated_descriptions = sum(count for count in descriptions.values() if count >= 2)
    return {
        "snapshot_posts": n,
        "distinct_sources": len(sources),
        "sources_top_10": [{"source_key": key, "posts": count} for key, count in sources.most_common(10)],
        "top_source_share_pct": round(100 * sources.most_common(1)[0][1] / n, 1) if n else None,
        "platform_counts": dict(platforms.most_common()),
        "empty_text": no_text,
        "literal_placeholder_text": placeholders,
        "very_short_nonplaceholder_text_under_6_words": short,
        "exact_duplicate_captions_extra_posts": duplicate_texts,
        "posts_with_repeated_long_description": repeated_descriptions,
        "has_image_local_reference": image_refs,
        "image_file_accessible": image_ok,
        "has_video_local_reference": video_refs,
        "video_file_accessible": video_ok,
        "top_data_fields": dict(data_keys.most_common(15)),
        "notes": [
            "This is corpus composition, NOT measured search quality or topic coverage.",
            "Placeholder and short captions cannot expose visual content to the text-only model.",
            "Image/video files are checked only under the chosen media root; URLs are never fetched.",
            "Source/topic model tags are NOT independent relevance gold.",
        ],
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", default="data/embeddinggemma2-benchmark")
    parser.add_argument("--media-dir", default=os.getenv("POSTA_MEDIA_DIR", "/var/lib/posta/media"))
    parser.add_argument("--media-url", default=os.getenv("POSTA_MEDIA_URL", "media"))
    args = parser.parse_args()
    folder = Path(args.output_dir)
    rows = read_jsonl(folder / "snapshot.jsonl")
    if not rows:
        raise SystemExit("Snapshot has no records")
    result = audit(rows, Path(args.media_dir), args.media_url)
    output = folder / "corpus_audit.json"
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    output.chmod(0o600)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
