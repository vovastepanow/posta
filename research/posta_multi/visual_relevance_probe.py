#!/usr/bin/env python3
"""Reproducible, manually image-judged 12-item Posta visual retrieval probe.

Labels are derived from visual inspection of contact_sheet.jpg, NOT from
pre-existing model-generated topic labels or text captions. The original visual
smoke snapshot was assembled in deterministic numbered order. This probe checks
the expected source identity at each position to prevent accidental relabeling.
The metrics describe only these 12 sampled images, never overall search quality.
"""
from __future__ import annotations

import argparse
from collections import defaultdict
import json
from pathlib import Path
import statistics

from benchmark_embed import caption, read_jsonl, write_jsonl
from visual_contact_sheet import safe_image_path

# Hand-verified from the 12-image contact sheet shown in the conversation.
# Deliberately distinguish visual features rather than quoting source captions.
ITEMS = (
    ("rss:https://mastodon.social/@imemeit_bot.rss",
     "Аниме девушка с короткими голубыми волосами закрывает лицо ладонью"),
    ("rss:https://mastodon.social/@optimatesocial.rss",
     "Скриншот сервиса планирования публикаций с синими кнопками"),
    ("rss:https://mastodon.social/@thesocialmediatoolsguy.rss",
     "Молодой человек в чёрной одежде машет рукой рядом с большим микрофоном"),
    ("rss:https://pikabu.ru/community/animemems",
     "Двухпанельный аниме комикс с рыжеволосой девушкой и красной кнопкой"),
    ("rss:https://vc.ru/rss",
     "Красная карта криптовалютного рынка с большой надписью BTC"),
    ("rss:https://www.kolesa.ru/export/rss.xml",
     "Красный внедорожный пожарный грузовик Mercedes на лесной дороге"),
    ("vk:british_humor",
     "Знаменитая часовая башня Биг Бен в Лондоне"),
    ("vk:leprum", None),  # ambiguous low-resolution artwork; not judged
    ("vk:memes_2024",
     "Жёлтый смайлик со слезами от смеха"),
    ("vk:mi.tut.prosta2",
     "Крупный план морды серого кота с большими глазами"),
    ("vk:mrzlk", None),   # ambiguous low-resolution poster; not judged
    ("vk:sci_one",
     "Силуэт человеческой головы с изображением земного шара внутри"),
)


def checked_rows(folder: Path) -> list[dict]:
    rows = read_jsonl(folder / "snapshot.jsonl")
    if len(rows) != len(ITEMS):
        raise ValueError(f"Expected precisely {len(ITEMS)} numbered visual posts, got {len(rows)}")
    ids = [r.get("id") for r in rows]
    if any(not isinstance(pid, str) for pid in ids) or len(set(ids)) != len(ids):
        raise ValueError("Snapshot contains missing/duplicate post identifiers")
    for i, (row, (expected_source, _)) in enumerate(zip(rows, ITEMS), 1):
        if row.get("source_key") != expected_source:
            raise ValueError(
                f"Image #{i} source mismatch: expected {expected_source!r}; "
                "this visual review belongs to a DIFFERENT snapshot"
            )
        if safe_image_path(row, folder) is None:
            raise ValueError(f"Local image #{i} is missing or outside the sample directory")
    return rows


def gold_rows(rows: list[dict]) -> list[dict]:
    return [
        {
            "query": query,
            "relevant_ids": [rows[i]["id"]],
            "judged_complete": True,
            "visual_target_number": i + 1,
            "judgment_source": "manual inspection of 12-image contact sheet",
        }
        for i, (_, query) in enumerate(ITEMS) if query is not None
    ]


def prepare(folder: Path) -> Path:
    rows = checked_rows(folder)
    dest = folder / "visual_gold.jsonl"
    write_jsonl(dest, gold_rows(rows))
    print(f"Prepared {len(gold_rows(rows))} visually judged questions in {dest}.")
    print("Skipped ambiguous low-resolution images #8 and #11.")
    return dest


def score(folder: Path) -> dict:
    rows = checked_rows(folder)
    gold = gold_rows(rows)
    metrics_path = folder / "metrics.json"
    candidates_path = folder / "candidates.jsonl"
    if not metrics_path.is_file() or not candidates_path.is_file():
        raise FileNotFoundError("Run benchmark_embed.py with --labels visual_gold.jsonl first")
    m = json.loads(metrics_path.read_text(encoding="utf-8"))
    kinds = m.get("kinds") or {}
    if (
        m.get("text_only") is not False
        or m.get("selected_rows") != 12
        or m.get("indexed") != 12
        or m.get("snapshot_rows") != 12
        or kinds.get("image", 0) + kinds.get("image+text", 0) != 12
        or m.get("queries") != len(gold)
    ):
        raise ValueError("Expected completed 12-image multimodal benchmark of these 10 queries")
    grouped = defaultdict(list)
    for candidate in read_jsonl(candidates_path):
        grouped[candidate["query"]].append(candidate)
    expected_queries = {g["query"] for g in gold}
    if set(grouped) != expected_queries:
        raise ValueError("Candidates do not match the visually reviewed queries")
    outcomes = []
    for g in gold:
        q = g["query"]
        ranked = sorted(grouped[q], key=lambda x: x["rank"])
        ranks = [r["rank"] for r in ranked]
        ids = [r["post_id"] for r in ranked]
        if ranks != list(range(1, 13)) or set(ids) != {row["id"] for row in rows}:
            raise ValueError(f"Invalid candidate ranking for {q!r}")
        expected = g["relevant_ids"][0]
        position = ids.index(expected) + 1
        number = g["visual_target_number"]
        mode = "image_only" if not caption(rows[number - 1]["data"]) else "image_plus_text"
        outcomes.append({
            "query": q, "target_image_number": number, "target_rank": position,
            "top1": position == 1, "top3": position <= 3, "mode": mode,
            "predicted_top1_image_number": next(
                i + 1 for i, r in enumerate(rows) if r["id"] == ids[0]
            ),
        })
    def aggregate(items):
        if not items:
            return {"queries": 0, "hit_at_1": None, "hit_at_3": None, "mrr": None}
        n = len(items)
        return {
            "queries": n,
            "hit_at_1": round(sum(x["top1"] for x in items) / n, 4),
            "hit_at_3": round(sum(x["top3"] for x in items) / n, 4),
            "mrr": round(statistics.mean(1 / x["target_rank"] for x in items), 4),
        }
    result = {
        "scope": "visual review of 10 targets among a fixed 12-image sample, NOT general model accuracy",
        "overall": aggregate(outcomes),
        "image_only": aggregate([x for x in outcomes if x["mode"] == "image_only"]),
        "image_plus_text": aggregate([x for x in outcomes if x["mode"] == "image_plus_text"]),
        "per_query": outcomes,
        "notes": [
            "Reference labels are based on visual inspection of the user's contact sheet.",
            "Images #8 and #11 excluded because their meaning is ambiguous at preview resolution.",
            "The 12-item sample is too small and nonrepresentative for production quality claims.",
            "No image-free baseline evaluated here; image+text results may reflect caption cues.",
        ],
    }
    output = folder / "visual_probe_results.json"
    output.write_text(json.dumps(result, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    output.chmod(0o600)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", choices=("prepare", "score"), required=True)
    parser.add_argument("--output-dir", default="data/embeddinggemma2-visual-smoke")
    args = parser.parse_args()
    folder = Path(args.output_dir)
    if args.phase == "prepare":
        prepare(folder)
    else:
        score(folder)


if __name__ == "__main__":
    main()
