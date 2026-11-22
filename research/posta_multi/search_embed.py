#!/usr/bin/env python3
"""Local Posta Embed search MVP: real public posts, cached neural embeddings.

This is an opt-in CLI prototype, NOT a production HTTP endpoint. No DB writes,
no remote media fetches, no GPU rental, no automatic background hooks. The full
text mode uses the 733-post snapshot (and excludes content-free captions).
Visual mode uses the separately saved 12-image sample.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re

from benchmark_embed import (
    MODEL_ID, _normalize, caption, load_model, read_jsonl, snapshot_fingerprint,
)
from benchmark_lexical import build_bm25, score_bm25
from audit_retrieval_corpus import PLACEHOLDER

WHITESPACE = re.compile(r"\s+")
RRF_K = 60


def useful_text(text: str) -> bool:
    return bool(text.strip()) and not PLACEHOLDER.fullmatch(text.strip())


def post_link(row: dict) -> str:
    data = row.get("data") or {}
    for field in ("url", "source_url"):
        u = data.get(field)
        if isinstance(u, str) and u.startswith(("https://", "http://")):
            return u
    return ""


def select_rankings(
    records: list[dict], neural_scores: list[float], *,
    query: str, mode: str, limit: int, neural_weight: float = .85,
    per_source: int = 2,
) -> list[dict]:
    """Fuse semantic and lexical ranks, with source diversity and text quality.

    All score inputs are from independently precomputed embeddings; this routine
    makes no remote calls and can be regression-tested with tiny fixtures.
    """
    if len(records) != len(neural_scores) or len(records) == 0:
        raise ValueError("Records and neural scores must have equal, nonzero length")
    if len({r["id"] for r in records}) != len(records):
        raise ValueError("Duplicate public-post IDs")
    if not 0 <= neural_weight <= 1 or limit <= 0 or per_source <= 0:
        raise ValueError("Invalid ranking configuration")

    eligible = [
        i for i, row in enumerate(records)
        if mode == "visual" or useful_text(caption(row["data"]))
    ]
    if not eligible:
        return []
    # Semantic similarity alone can overrank generic placeholders; text mode
    # excludes them rather than claiming they contain searchable meaning.
    neural = sorted(eligible, key=lambda i: (-neural_scores[i], i))
    scores = {i: neural_weight / (RRF_K + rank)
              for rank, i in enumerate(neural, 1)}
    if mode == "text":
        lexical = score_bm25(query, build_bm25(
            [caption(row["data"]) if i in eligible else "" for i, row in enumerate(records)]
        ))
        positive = sorted((i for i in eligible if lexical[i] > 0),
                          key=lambda i: (-lexical[i], i))
        for rank, i in enumerate(positive, 1):
            scores[i] += (1 - neural_weight) / (RRF_K + rank)

    ordered = sorted(eligible, key=lambda i: (-scores[i], -neural_scores[i], i))
    by_source, seen_text = {}, set()
    selected = []
    for i in ordered:
        row = records[i]
        source = row.get("source_key") or "(unknown)"
        if by_source.get(source, 0) >= per_source:
            continue
        text = caption(row["data"])
        key = WHITESPACE.sub(" ", text.casefold()).strip()
        if mode == "text" and key in seen_text:
            continue
        by_source[source] = by_source.get(source, 0) + 1
        if mode == "text":
            seen_text.add(key)
        selected.append({
            "post_id": row["id"],
            "source_key": source,
            "platform": row.get("platform"),
            "url": post_link(row),
            "text_preview": WHITESPACE.sub(" ", text).strip()[:260],
            "cosine": round(neural_scores[i], 4),
            "fusion_score": round(scores[i], 6),
        })
        if len(selected) == limit:
            break
    return selected


def search(folder: Path, query: str, mode: str, count: int, device: str) -> dict:
    import numpy as np

    folder = folder.resolve()
    rows = read_jsonl(folder / "snapshot.jsonl")
    if not rows:
        raise ValueError("Empty local snapshot")
    text_only = mode == "text"
    media_dir = folder / "media" if not text_only else Path("media_cache")
    media_url = "media" if not text_only else "media_cache"
    fingerprint = snapshot_fingerprint(rows, media_dir, media_url, False, 4, text_only)

    path = folder / "embeddings.npz"
    if not path.is_file():
        raise FileNotFoundError(f"No precomputed vectors in {path}. Run benchmark_embed.py first.")
    with np.load(path, allow_pickle=False) as cached:
        if str(cached["fingerprint"].item()) != fingerprint:
            raise ValueError("Embedding cache does not belong to this snapshot/mode; reindex it first")
        ids = cached["ids"].tolist()
        vectors = np.asarray(cached["vectors"], dtype=np.float32)
    by_id = {r["id"]: r for r in rows}
    if len(ids) != len(set(ids)) or set(ids) != set(by_id):
        raise ValueError("Embedding cache is incomplete or refers to a different snapshot")
    records = [by_id[identifier] for identifier in ids]
    if vectors.ndim != 2 or len(vectors) != len(records):
        raise ValueError("Corrupted embedding matrix")

    model, resolved_device, dtype = load_model(device, text_only=text_only)
    query_vec = _normalize(model.encode(
        query, prompt_name="SearchQuery", normalize_embeddings=True,
    ))
    if query_vec.shape[0] != vectors.shape[1]:
        raise ValueError("Query and document embeddings have different dimensions")
    similarities = (vectors @ query_vec).tolist()
    selected = select_rankings(records, similarities, query=query, mode=mode, limit=count)
    return {
        "query": query,
        "mode": mode,
        "indexed_posts": len(records),
        "candidates_after_text_filter": sum(useful_text(caption(r["data"])) for r in records)
        if text_only else len(records),
        "model": MODEL_ID,
        "device": resolved_device,
        "dtype": dtype,
        "retrieval": "reciprocal rank fusion BM25 + neural" if text_only else "neural image/text vectors",
        "results": selected,
        "warning": "Prototype results are unverified candidates; no production accuracy claim.",
    }


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("query", help="Natural language query; quote if it contains spaces")
    parser.add_argument("--mode", choices=("text", "visual"), default="text")
    parser.add_argument("--text-dir", default="data/embeddinggemma2-benchmark")
    parser.add_argument("--visual-dir", default="data/embeddinggemma2-visual-smoke")
    parser.add_argument("--top", type=int, default=10)
    parser.add_argument("--device", default="cpu")
    args = parser.parse_args()
    if not 1 <= args.top <= 100:
        parser.error("--top must be between 1 and 100")
    if not args.query.strip():
        parser.error("query may not be empty")
    folder = Path(args.text_dir if args.mode == "text" else args.visual_dir)
    response = search(folder, args.query, args.mode, args.top, args.device)
    print(json.dumps(response, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
