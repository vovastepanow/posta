#!/usr/bin/env python3
"""Compare Posta Embed against lexical BM25 on the SAME exported public-post corpus.

This reports top results and overlap, NOT accuracy. Accuracy requires independently
judged relevant/irrelevant results. Does not load models or connect to Postgres.
"""
from __future__ import annotations

import argparse
from collections import defaultdict
import json
from pathlib import Path
import re

from benchmark_embed import read_jsonl


def grouped(path: Path) -> dict[str, list[dict]]:
    if not path.is_file():
        raise FileNotFoundError(f"Missing {path}. Run the corresponding benchmark first.")
    groups: dict[str, list[dict]] = defaultdict(list)
    for row in read_jsonl(path):
        query, post_id, rank = row.get("query"), row.get("post_id"), row.get("rank")
        if not isinstance(query, str) or not isinstance(post_id, str) or not isinstance(rank, int):
            raise ValueError(f"Malformed candidate in {path}")
        groups[query].append(row)
    for query, records in groups.items():
        records.sort(key=lambda r: r["rank"])
        ranks = [r["rank"] for r in records]
        ids = [r["post_id"] for r in records]
        if ranks != list(range(1, len(ranks) + 1)) or len(set(ids)) != len(ids):
            raise ValueError(f"Duplicate or missing ranks/post IDs for {query!r} in {path}")
    return dict(groups)


def preview(row: dict, limit: int) -> str:
    raw = row.get("text_preview") or "(нет текстового описания)"
    compact = re.sub(r"\s+", " ", raw).strip()
    return compact[:limit] + ("…" if len(compact) > limit else "")


def validate_same_run(folder: Path) -> None:
    neural_metrics = folder / "metrics.json"
    lexical_metrics = folder / "lexical_metrics.json"
    if not neural_metrics.is_file() or not lexical_metrics.is_file():
        raise FileNotFoundError("Need metrics.json and lexical_metrics.json; run both benchmarks first.")
    neural = json.loads(neural_metrics.read_text(encoding="utf-8"))
    lexical = json.loads(lexical_metrics.read_text(encoding="utf-8"))
    if not (
        neural.get("text_only") is True
        and neural.get("selected_rows") == neural.get("snapshot_rows")
        and neural.get("indexed") == neural.get("snapshot_rows")
        and lexical.get("snapshot_posts") == neural.get("snapshot_rows")
        and neural.get("queries") == lexical.get("queries")
    ):
        raise ValueError(
            "Neural benchmark must index the full text-only snapshot, and both "
            "benchmarks must use the same corpus/query counts. Re-run them."
        )


def report(folder: Path, query_limit: int, top: int, width: int = 190) -> str:
    validate_same_run(folder)
    neural = grouped(folder / "candidates.jsonl")
    lexical = grouped(folder / "lexical_candidates.jsonl")
    if set(neural) != set(lexical):
        raise ValueError("Neural and BM25 candidates must cover the same queries")
    if not neural:
        raise ValueError("No benchmark queries")
    lines = [
        "# Posta: EmbeddingGemma 2 vs BM25",
        "",
        "**Это сравнение выдачи, а не оценка точности.** "
        "Оценки релевантности пока не размечены вручную.",
        "",
    ]
    queries = list(neural)
    for query in queries[:query_limit]:
        e = neural[query]
        b = lexical[query]
        if len(e) < 10 or len(b) < 10:
            raise ValueError(f"Too few candidates for {query!r} to compare top 10")
        common = len({r["post_id"] for r in e[:10]} & {r["post_id"] for r in b[:10]})
        lines.extend([f"## {query}", "", f"Общие результаты в топ-10: **{common}/10** (не метрика качества).", ""])
        for name, results in (("EmbeddingGemma 2", e), ("BM25", b)):
            lines.append(f"**{name}**")
            lines.append("")
            for row in results[:top]:
                score = row.get("score_cosine") if name == "EmbeddingGemma 2" else row.get("score_bm25")
                value = f" [score {score:.3f}]" if isinstance(score, (int, float)) else ""
                lines.append(f'{row["rank"]}. {preview(row, width)}{value}')
            lines.append("")
    if query_limit < len(queries):
        lines.append(f"_Показано запросов: {query_limit} из {len(queries)}._")
    return "\n".join(lines).rstrip() + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", default="data/embeddinggemma2-benchmark")
    parser.add_argument("--queries", type=int, default=3, help="First N queries to display (1..24)")
    parser.add_argument("--top", type=int, default=5, help="Results per method (1..10)")
    args = parser.parse_args()
    if not 1 <= args.queries <= 24 or not 1 <= args.top <= 10:
        parser.error("--queries must be 1..24 and --top 1..10")
    folder = Path(args.output_dir)
    content = report(folder, args.queries, args.top)
    path = folder / "comparison.md"
    path.write_text(content, encoding="utf-8")
    path.chmod(0o600)
    print(content)
    print(f"Local report: {path}")


if __name__ == "__main__":
    main()
