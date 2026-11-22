#!/usr/bin/env python3
"""Isolated EmbeddingGemma 2 retrieval benchmark for Posta.

Phase 1: READ-ONLY PostgreSQL export of public posts.
Phase 2: local text/image/video embeddings, ranking, optional gold evaluation.
No DB mutations, no API calls, no training, no automatic deploy hooks.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics
import sys
import time

MODEL_ID = "google/embeddinggemma-2"
VERSION = 1
MEDIA_NAME = re.compile(r"^[a-f0-9]{32}\.[a-z0-9]{2,5}$")
IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".webp"}
VIDEO_EXTS = {".mp4", ".mov", ".mkv", ".webm"}
EXAMPLE_QUERIES = [
    "Формула-1: результаты гонок и новости команд",
    "Личные финансы и финансовая грамотность без рекламы",
    "Новости искусственного интеллекта",
    "Кот за компьютером",
    "Мем с надписью на изображении",
    "Дрифт зимой на снегу",
    "Новые автомобили и автоспорт",
    "Полезные советы по тренировкам",
    "Криптовалюты и блокчейн",
    "Малый бизнес и предпринимательство",
    "Новые научные открытия",
    "Футбольные матчи и забитые голы",
    "Объявления о продаже под видом новостей",
    "Реклама казино или ставок",
    "Смешной ролик с собакой",
    "Путешествия и красивые природные места",
    "Актуальные новости технологий",
    "Работа и карьера в IT",
    "Рецепты домашней еды",
    "Ремонт и интерьер квартиры",
    "Иллюстрации и цифровое искусство",
    "Юмор о работе и офисе",
    "Подробный обзор смартфона",
    "Сюжетное видео без текстового описания",
]


def post_id(source_key: str, post_key: str) -> str:
    """Lossless composite ID; post_key alone is not globally unique."""
    return json.dumps(["public", source_key, post_key], ensure_ascii=False, separators=(",", ":"))


def caption(data: dict) -> str:
    candidates = [data.get("text"), data.get("description")]
    caps = data.get("captions")
    if isinstance(caps, list) and caps:
        candidates.append(caps[0])
    parts = []
    for candidate in candidates:
        if isinstance(candidate, str) and candidate.strip() and candidate.strip() not in parts:
            parts.append(candidate.strip())
    return "\n".join(parts)[:3000]


def local_media(data: dict, media_dir: Path, media_url: str) -> tuple[str | None, str | None]:
    """Map the same local URL convention used by App.MediaPath in Go.

    Never fetch remote URLs and never open paths outside POSTA_MEDIA_DIR.
    """
    root = media_dir.resolve()
    prefix = media_url.strip("/") + "/"

    def resolve_url(url: object, extensions: set[str]) -> str | None:
        if not isinstance(url, str) or not url.startswith(prefix):
            return None
        name = url[len(prefix):]
        if not MEDIA_NAME.fullmatch(name) or Path(name).suffix not in extensions:
            return None
        candidate = root / name
        try:
            actual = candidate.resolve(strict=True)
            if actual.parent == root and actual.is_file():
                return str(actual)
        except (OSError, RuntimeError):
            pass
        return None

    entries = [data]
    if isinstance(data.get("gallery"), list):
        entries.extend(item for item in data["gallery"] if isinstance(item, dict))
    image = video = None
    for entry in entries:
        if image is None:
            image = resolve_url(entry.get("image_local"), IMAGE_EXTS)
        if video is None:
            video = resolve_url(entry.get("video_local"), VIDEO_EXTS)
        if image and video:
            break
    return image, video


def content_input(record: dict, media_dir: Path, media_url: str, include_video: bool) -> tuple[str | dict | None, str]:
    """One native joint embedding per post, not a mean of unrelated vectors."""
    text_value = caption(record["data"])
    image, video = local_media(record["data"], media_dir, media_url)
    if include_video and video:
        if text_value:
            return {"text": text_value + "\n<|video|>", "video": video}, "video+text"
        return {"video": video}, "video"
    if image:
        if text_value:
            return {"text": text_value + "\n<|image|>", "image": image}, "image+text"
        return {"image": image}, "image"
    if text_value:
        return text_value, "text"
    return None, "missing"


def _decode_json(value):
    if isinstance(value, (bytes, bytearray)):
        value = value.decode("utf-8")
    if isinstance(value, str):
        return json.loads(value)
    return value


def export_public_posts(dsn: str, limit: int) -> list[dict]:
    """SELECT only, inside a PostgreSQL read-only transaction."""
    import psycopg2

    connection = psycopg2.connect(dsn, connect_timeout=10)
    connection.set_session(readonly=True, autocommit=False)
    try:
        with connection.cursor() as cursor:
            cursor.execute("SET LOCAL statement_timeout = '30000ms'")
            cursor.execute(
                """
                SELECT p.source_key, p.post_key, p.platform, p.data,
                       COALESCE((
                           SELECT jsonb_agg(t.topic_key ORDER BY t.topic_key)
                           FROM public_post_topics t
                           WHERE t.source_key = p.source_key AND t.post_key = p.post_key
                       ), '[]'::jsonb)
                FROM public_posts AS p
                WHERE COALESCE(p.data->>'nsfw', 'false') <> 'true'
                  AND COALESCE(p.data->>'source_sensitive', 'false') <> 'true'
                ORDER BY p.last_seen DESC, p.source_key, p.post_key
                LIMIT %s
                """,
                (limit,),
            )
            rows = cursor.fetchall()
        result = [
            {"id": post_id(src, key), "source_key": src, "post_key": key,
             "platform": platform, "data": _decode_json(data),
             "existing_topic_hints": _decode_json(tags)}
            for src, key, platform, data, tags in rows
        ]
        return result
    finally:
        connection.rollback()
        connection.close()


def write_jsonl(path: Path, rows: list[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with path.open("w", encoding="utf-8") as stream:
        for row in rows:
            stream.write(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n")
    path.chmod(0o600)


def read_jsonl(path: Path) -> list[dict]:
    with path.open(encoding="utf-8") as stream:
        return [json.loads(line) for line in stream if line.strip()]


def label_template(path: Path) -> None:
    if not path.exists():
        write_jsonl(path, [
            {"query": query, "relevant_ids": [], "judged_complete": False}
            for query in EXAMPLE_QUERIES
        ])


def _normalize(vector):
    import numpy as np

    x = np.asarray(vector, dtype=np.float32).reshape(-1)
    norm = float(np.linalg.norm(x))
    if not np.isfinite(norm) or norm < 1e-8:
        raise ValueError("Invalid or zero model embedding")
    return x / norm


def load_model(device: str):
    import torch
    from sentence_transformers import SentenceTransformer

    resolved = "cuda" if device == "auto" and torch.cuda.is_available() else device
    if resolved == "auto":
        resolved = "cpu"
    dtype = torch.bfloat16 if resolved.startswith("cuda") and torch.cuda.is_bf16_supported() else torch.float32
    if resolved.startswith("cuda") and dtype == torch.float32:
        print("WARNING: GPU lacks BF16; FP32 may need substantially more VRAM.", file=sys.stderr)
    model = SentenceTransformer(
        MODEL_ID,
        device=resolved,
        config_kwargs={"audio_config": None},  # 440M text+vision: speech benchmark is a later stage
        model_kwargs={"torch_dtype": dtype},
    )
    return model, resolved, str(dtype)


def metric_values(ranking: list[str], relevant: set[str]) -> dict:
    if not relevant:
        raise ValueError("Gold query must have >=1 relevant document")
    hits = len(set(ranking[:100]) & relevant)
    dcg = sum(1 / math.log2(index + 2) for index, item in enumerate(ranking[:10]) if item in relevant)
    idcg = sum(1 / math.log2(index + 2) for index in range(min(10, len(relevant))))
    return {"recall_at_100": hits / len(relevant), "ndcg_at_10": dcg / idcg}


def evaluate_rankings(gold_rows: list[dict], rankings: dict[str, list[str]], valid_ids: set[str]) -> dict:
    per_query = []
    skipped = 0
    for item in gold_rows:
        query = item.get("query")
        if not isinstance(query, str) or query not in rankings:
            skipped += 1
            continue
        if item.get("judged_complete") is not True:
            skipped += 1
            continue
        relevant = set(item.get("relevant_ids") or [])
        if not relevant:
            skipped += 1
            continue
        missing = relevant - valid_ids
        if missing:
            raise ValueError(
                f"Ground truth for {query!r} includes {len(missing)} absent/non-indexed IDs. "
                "Regenerate labels for this snapshot or correct them."
            )
        per_query.append({"query": query, **metric_values(rankings[query], relevant)})
    result = {"evaluated_queries": len(per_query), "skipped_queries": skipped, "per_query": per_query}
    if per_query:
        result["recall_at_100"] = statistics.mean(x["recall_at_100"] for x in per_query)
        result["ndcg_at_10"] = statistics.mean(x["ndcg_at_10"] for x in per_query)
    else:
        result["recall_at_100"] = None
        result["ndcg_at_10"] = None
        result["warning"] = "No complete, manually validated gold judgments; no retrieval-quality claim."
    return result


def percentile_ms(values: list[float], percentile: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    index = (len(ordered) - 1) * percentile
    lower = int(index)
    upper = min(lower + 1, len(ordered) - 1)
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (index - lower)


def snapshot_fingerprint(records: list[dict], media_dir: Path, media_url: str, include_video: bool, max_frames: int) -> str:
    digest = hashlib.sha256()
    digest.update(f"{MODEL_ID}|{VERSION}|{include_video}|{max_frames}".encode())
    for row in records:
        digest.update(json.dumps(row, sort_keys=True, ensure_ascii=False).encode())
        image, video = local_media(row["data"], media_dir, media_url)
        for path in (image, video if include_video else None):
            if path:
                stat = Path(path).stat()
                digest.update(f"{path}|{stat.st_size}|{stat.st_mtime_ns}".encode())
    return digest.hexdigest()


def build_index(records: list[dict], model, args, folder: Path):
    import numpy as np

    media_dir = Path(args.media_dir)
    fingerprint = snapshot_fingerprint(records, media_dir, args.media_url, args.include_video, args.max_frames)
    cache_path = folder / "embeddings.npz"
    if cache_path.exists() and not args.no_cache:
        with np.load(cache_path, allow_pickle=False) as cached:
            if str(cached["fingerprint"].item()) == fingerprint:
                return cached["ids"].tolist(), cached["vectors"].astype(np.float32), cached["kinds"].tolist(), 0.0, True
    ids, vectors, kinds = [], [], []
    started = time.perf_counter()
    for row_number, row in enumerate(records, 1):
        payload, kind = content_input(row, media_dir, args.media_url, args.include_video)
        if payload is None:
            continue
        options = {"normalize_embeddings": True, "show_progress_bar": False}
        if isinstance(payload, str):
            options["prompt_name"] = "Document"
        elif "video" in payload:
            options["processing_kwargs"] = {
                "video": {"fps": 1, "max_frames": args.max_frames, "overflow_strategy": "uniform"}
            }
        try:
            vector = _normalize(model.encode(payload, **options))
        except Exception as exc:
            print(f"SKIP {row_number}/{len(records)} {kind}: {type(exc).__name__}: {exc}", file=sys.stderr)
            continue
        ids.append(row["id"])
        vectors.append(vector)
        kinds.append(kind)
        if row_number % 100 == 0:
            print(f"indexed {row_number}/{len(records)}; valid={len(ids)}", flush=True)
    elapsed = time.perf_counter() - started
    if not vectors:
        raise RuntimeError("No embeddings: check dataset, local media, model installation and memory.")
    matrix = np.vstack(vectors)
    tmp_path = folder / ".embeddings.npz.tmp"
    with tmp_path.open("wb") as stream:
        np.savez_compressed(stream, ids=np.asarray(ids), vectors=matrix,
                            kinds=np.asarray(kinds), fingerprint=np.asarray(fingerprint))
    tmp_path.chmod(0o600)
    tmp_path.replace(cache_path)
    return ids, matrix, kinds, elapsed, False


def run(args) -> None:
    import numpy as np

    folder = Path(args.output_dir).expanduser().resolve()
    if not folder.is_dir():
        raise SystemExit("Export a dataset first with --phase snapshot.")
    rows = read_jsonl(folder / "snapshot.jsonl")
    model, device, dtype = load_model(args.device)
    ids, vectors, kinds, index_seconds, cache_used = build_index(rows, model, args, folder)
    id_to_row = {row["id"]: row for row in rows}
    label_path = Path(args.labels).expanduser() if args.labels else folder / "labels_template.jsonl"
    if not label_path.is_file():
        raise SystemExit(f"Labels file not found: {label_path}")
    gold = read_jsonl(label_path)
    queries = list(dict.fromkeys(row["query"] for row in gold if isinstance(row.get("query"), str) and row["query"].strip()))
    if not queries:
        raise SystemExit("No queries in labels file.")
    rankings = {}
    times = []
    candidates = []
    for query in queries:
        started = time.perf_counter()
        q = _normalize(model.encode(query, prompt_name="SearchQuery", normalize_embeddings=True))
        scores = vectors @ q
        order = np.argsort(-scores, kind="stable")
        times.append((time.perf_counter() - started) * 1000)
        ranked = [ids[int(i)] for i in order]
        rankings[query] = ranked
        for rank, index in enumerate(order[:min(100, len(order))], 1):
            post = id_to_row[ids[int(index)]]
            candidates.append({
                "query": query, "rank": rank, "post_id": post["id"],
                "score_cosine": float(scores[int(index)]), "kind": kinds[int(index)],
                "platform": post["platform"], "text_preview": caption(post["data"])[:400],
                "existing_topic_hints_not_gold": post.get("existing_topic_hints", []),
            })
    write_jsonl(folder / "candidates.jsonl", candidates)
    metrics = evaluate_rankings(gold, rankings, set(ids))
    metrics.update({
        "model": MODEL_ID, "version": VERSION, "device": device, "dtype": dtype,
        "snapshot_rows": len(rows), "indexed": len(ids), "skipped": len(rows) - len(ids),
        "kinds": {kind: kinds.count(kind) for kind in sorted(set(kinds))},
        "index_seconds": round(index_seconds, 3),
        "posts_per_second": round(len(ids) / index_seconds, 3) if index_seconds > 0 else None,
        "index_cached": cache_used, "queries": len(queries),
        "latency_mean_ms": round(statistics.mean(times), 3),
        "latency_p50_ms": round(percentile_ms(times, .5), 3),
        "latency_p95_ms": round(percentile_ms(times, .95), 3),
        "latency_note": "Includes query embedding and exact NumPy matrix search; not production ANN/network latency.",
        "video": args.include_video, "max_video_frames": args.max_frames,
    })
    output = folder / "metrics.json"
    output.write_text(json.dumps(metrics, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    output.chmod(0o600)
    print(json.dumps(metrics, indent=2, ensure_ascii=False))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", choices=["snapshot", "benchmark"], required=True)
    parser.add_argument("--output-dir", default="data/embeddinggemma2-benchmark")
    parser.add_argument("--limit", type=int, default=2000)
    parser.add_argument("--media-dir", default=os.environ.get("POSTA_MEDIA_DIR", "media_cache"))
    parser.add_argument("--media-url", default=os.environ.get("POSTA_MEDIA_URL", "media_cache"))
    parser.add_argument("--labels", help="Gold JSONL; default generated unjudged template.")
    parser.add_argument("--include-video", action="store_true", help="Enable direct local video embeddings.")
    parser.add_argument("--max-frames", type=int, default=4, help="Native video sampler frame cap.")
    parser.add_argument("--device", default="auto", help="auto, cpu, or cuda")
    parser.add_argument("--no-cache", action="store_true")
    args = parser.parse_args(argv)
    if not 1 <= args.limit <= 10000 or not 1 <= args.max_frames <= 16:
        parser.error("--limit must be 1..10000; --max-frames must be 1..16")
    folder = Path(args.output_dir).expanduser()
    if args.phase == "snapshot":
        dsn = os.environ.get("POSTA_PG")
        if not dsn:
            parser.error("POSTA_PG is required; never hardcode credentials in source files.")
        folder.mkdir(parents=True, exist_ok=True, mode=0o700)
        folder.chmod(0o700)
        rows = export_public_posts(dsn, args.limit)
        if not rows:
            raise SystemExit("No public posts found; no synthetic replacements were fabricated.")
        write_jsonl(folder / "snapshot.jsonl", rows)
        label_template(folder / "labels_template.jsonl")
        print(f"Exported {len(rows)} public posts to {folder / 'snapshot.jsonl'}; read-only transaction.")
    else:
        run(args)


if __name__ == "__main__":
    main()
