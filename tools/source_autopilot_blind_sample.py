#!/usr/bin/env python3
"""Prepare a reproducible blind annotation set from existing public fixtures.

This is not a benchmark: no source decisions or model verdicts are used as
labels. The original fixture files remain the source of the snapshots.
"""
import argparse
import hashlib
import json
import random
from pathlib import Path

ROOT = Path(__file__).resolve().parent
FIXTURES = ROOT / "fixtures"
PROFILES = ["Технологии", "Роботакси", "Новости Москвы", "Мемы"]
SOURCES = ["source_preview_rss.json", "source_preview_tg_text.json", "source_preview_tg_memes.json"]


def main():
    # Never overwrite a copy which may already contain human annotations.
    parser = argparse.ArgumentParser()
    parser.add_argument("--stdout", action="store_true", help="print the generated sample without writing a file")
    args = parser.parse_args()
    rng = random.Random(20261114)
    samples = []
    for profile in PROFILES:
        for filename in SOURCES:
            source = json.loads((FIXTURES / filename).read_text(encoding="utf-8"))["discovered"]
            posts = source["posts"]
            for index in rng.sample(range(len(posts)), min(4, len(posts))):
                post = posts[index]
                identity = hashlib.sha256((filename + "\0" + str(index)).encode()).hexdigest()[:16]
                samples.append({
                    "profile": profile,
                    "fixture": filename,
                    "fixture_index": index,
                    "sample_id": identity,
                    "text": post.get("text", ""),
                    "posted_at": post.get("posted_at"),
                    "media_kinds": sorted({m.get("kind", "") for m in post.get("media", [])}),
                    "annotation": None,
                })
    rng.shuffle(samples)
    result = {"purpose": "blind human annotation; no quality claims", "seed": 20261114, "samples": samples}
    output = FIXTURES / "source_autopilot_blind_sample.json"
    rendered = json.dumps(result, ensure_ascii=False, indent=2) + "\n"
    if args.stdout:
        print(rendered, end="")
        return
    if output.exists():
        raise SystemExit(f"{output} already exists; use --stdout to regenerate without overwriting annotations")
    output.write_text(rendered, encoding="utf-8")
    print(f"{len(samples)} blind samples across {len(PROFILES)} profiles: {output}")


if __name__ == "__main__":
    main()
