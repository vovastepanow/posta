"""Evaluate frozen Posta embeddings without modifying checkpoint weights.

NPZ must include queries and posts of matching [N, D] shape, and may
include reversed_posts to test sensitivity to reversed event order.
Rows must be correctly paired, held-out, and de-duplicated upstream.
"""
import argparse
import hashlib
import json
from pathlib import Path

import numpy as np

from embedding_diagnostics import geometry, paired_alignment, transformation_sensitivity


def evaluate_npz(path):
    path = Path(path)
    with path.open("rb") as stream:
        sha = hashlib.file_digest(stream, "sha256").hexdigest()
    with np.load(path, allow_pickle=False) as data:
        if "queries" not in data or "posts" not in data:
            raise ValueError("NPZ needs queries and posts arrays")
        queries, posts = data["queries"], data["posts"]
        if (queries.ndim != 2 or posts.ndim != 2
                or queries.shape != posts.shape
                or queries.shape[0] < 2 or queries.shape[1] < 2):
            raise ValueError("queries and posts must have matching [N>=2, D>=2] shape")
        report = {
            "input_sha256": sha,
            "samples": int(queries.shape[0]),
            "dimensions": int(queries.shape[1]),
            "query_geometry": geometry(queries),
            "post_geometry": geometry(posts),
            "paired_retrieval": paired_alignment(queries, posts),
        }
        if "reversed_posts" in data:
            reversed_posts = data["reversed_posts"]
            if reversed_posts.shape != posts.shape:
                raise ValueError("reversed_posts shape must equal posts")
            report["temporal_order_sensitivity"] = transformation_sensitivity(
                posts, reversed_posts)
        return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--embeddings", required=True,
                        help="held-out NPZ with aligned queries/posts")
    parser.add_argument("--output",
                        help="optional JSON report destination (must not already exist)")
    args = parser.parse_args()
    report = evaluate_npz(args.embeddings)
    output = json.dumps(report, ensure_ascii=False, indent=2, allow_nan=False)
    if args.output:
        with Path(args.output).open("x", encoding="utf-8") as stream:
            stream.write(output + "\n")
    else:
        print(output)


if __name__ == "__main__":
    main()
