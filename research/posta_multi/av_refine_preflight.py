"""Read-only preflight for a larger frozen AV validation pool.

This verifies split isolation and actual available *screened* media records.
The rest of the 100GiB HDD corpus is not silently assumed to be AV training
data. No GPU or model weights are loaded here.
"""
import argparse
import json

from av_data import select_media_pairs
from corpus import Corpus


def preflight(dataset, limit=8):
    if limit < 8 or limit > 16:
        raise ValueError("requires 8-16 isolated held-out examples")
    corpus = Corpus(dataset)
    pool = {}
    for kind in ("video_text", "speech_text"):
        rows, digest = select_media_pairs(corpus, kind, "validation", limit)
        pool[kind] = {
            "records": len(rows),
            "groups": len({(r["dataset_id"], r["group_id"]) for r in rows}),
            "selection_sha256": digest,
        }
        if kind == "video_text" and pool[kind]["groups"] != len(rows):
            raise ValueError("video heldout examples share a source group")
    return {
        "format": "posta-av-235m-refine-preflight-v1",
        "manifest_sha256": corpus.manifest_hash,
        "heldout_limit": limit,
        "heldout": pool,
        "quality_verified": False,
        "production_promotion_allowed": False,
    }


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", required=True)
    parser.add_argument("--limit", type=int, default=8)
    args = parser.parse_args()
    print(json.dumps(preflight(args.dataset, args.limit), allow_nan=False), flush=True)
