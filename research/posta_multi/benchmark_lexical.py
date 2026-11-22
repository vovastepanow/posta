#!/usr/bin/env python3
"""Offline no-dependency BM25 baseline on an existing Posta public-post snapshot.

Compares against Posta Embed only on the same snapshot and with separately
human-reviewed relevance judgments. Never connects to Postgres or modifies Posta.
"""
from __future__ import annotations

import argparse
from collections import Counter
import json
import math
from pathlib import Path
import re
import statistics
import time

from benchmark_embed import caption, evaluate_rankings, read_jsonl, write_jsonl

TOKEN = re.compile(r"[^\W_]+", flags=re.UNICODE)


def tokenize(text: str) -> list[str]:
    """Deliberately plain multilingual lexical matching, without neural models."""
    return TOKEN.findall(text.casefold())


def build_bm25(texts: list[str]) -> tuple[list[Counter], list[int], dict[str, int], float]:
    frequencies = [Counter(tokenize(text)) for text in texts]
    lengths = [sum(counts.values()) for counts in frequencies]
    document_frequency: dict[str, int] = {}
    for counts in frequencies:
        for token in counts:
            document_frequency[token] = document_frequency.get(token, 0) + 1
    avg_len = sum(lengths) / len(lengths) if lengths else 0.0
    return frequencies, lengths, document_frequency, avg_len


def score_bm25(query: str, index: tuple, k1: float = 1.5, b: float = .75) -> list[float]:
    frequencies, lengths, df, avg_len = index
    count = len(frequencies)
    scores = [0.0] * count
    if count == 0:
        return scores
    unique_terms = set(tokenize(query))
    for term in unique_terms:
        docs = df.get(term, 0)
        if not docs:
            continue
        idf = math.log1p((count - docs + .5) / (docs + .5))
        for i, tf in enumerate(frequencies):
            freq = tf.get(term, 0)
            if freq:
                denom = freq + k1 * (1 - b + b * (lengths[i] / max(avg_len, 1e-9)))
                scores[i] += idf * freq * (k1 + 1) / denom
    return scores


def run(output: Path, label_path: Path | None, top: int) -> dict:
    source = output / "snapshot.jsonl"
    if not source.is_file():
        raise FileNotFoundError(f"Missing {source}; run read-only snapshot export first.")
    rows = read_jsonl(source)
    if not rows:
        raise ValueError("Empty snapshot")
    ids = [row["id"] for row in rows]
    if len(set(ids)) != len(ids):
        raise ValueError("Duplicate post IDs in snapshot")
    texts = [caption(row["data"]) for row in rows]
    start = time.perf_counter()
    index = build_bm25(texts)
    indexing_s = time.perf_counter() - start
    label_path = label_path or output / "labels_template.jsonl"
    if not label_path.is_file():
        raise FileNotFoundError(f"Missing queries: {label_path}")
    gold = read_jsonl(label_path)
    queries = list(dict.fromkeys(
        r["query"] for r in gold if isinstance(r.get("query"), str) and r["query"].strip()
    ))
    if not queries:
        raise ValueError("No queries in labels file")
    timings_ms = []
    rankings = {}
    candidates = []
    demos = []
    for query in queries:
        t0 = time.perf_counter()
        scores = score_bm25(query, index)
        order = sorted(range(len(rows)), key=lambda index: (-scores[index], index))
        timings_ms.append((time.perf_counter() - t0) * 1000)
        rankings[query] = [ids[i] for i in order]
        examples = []
        for rank, i in enumerate(order[:100], 1):
            row = rows[i]
            candidates.append({
                "query": query, "rank": rank, "post_id": ids[i], "score_bm25": scores[i],
                "text_preview": texts[i][:400], "platform": row.get("platform"),
                "existing_topic_hints_not_gold": row.get("existing_topic_hints", []),
            })
            if rank <= top:
                examples.append({
                    "rank": rank, "score": round(scores[i], 3),
                    "preview": texts[i][:120].replace("\n", " "),
                })
        demos.append({"query": query, "top": examples})
    write_jsonl(output / "lexical_candidates.jsonl", candidates)
    result = evaluate_rankings(gold, rankings, set(ids))
    result.update({
        "method": "BM25; exact lexical tokens, no stemming or neural model",
        "snapshot_posts": len(rows),
        "text_posts": sum(bool(t) for t in texts),
        "no_text_posts": sum(not t for t in texts),
        "index_seconds": round(indexing_s, 4),
        "queries": len(queries),
        "query_mean_ms": round(statistics.mean(timings_ms), 3),
        "latency_note": "Only local BM25 ranking on snapshot; excludes HTTP, Postgres and network.",
        "preview": demos[:3],
    })
    metrics = output / "lexical_metrics.json"
    metrics.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    metrics.chmod(0o600)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", default="data/embeddinggemma2-benchmark")
    parser.add_argument("--labels", default=None)
    parser.add_argument("--top", type=int, default=3, help="Show this many examples per query (0..10)")
    args = parser.parse_args()
    if not 0 <= args.top <= 10:
        parser.error("--top must be 0..10")
    run(Path(args.output_dir), Path(args.labels) if args.labels else None, args.top)


if __name__ == "__main__":
    main()
