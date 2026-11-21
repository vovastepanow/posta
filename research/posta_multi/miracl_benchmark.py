"""Freeze human-labeled MIRACL dev judged-pool evaluation for RU and EN.

Evaluation only. No pretrained weights, training, or whole MIRACL corpus.
The Dataset Server cannot be commit pinned; bracket with HF source HEAD
revision checks and freeze the actual selection with SHA256.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import random
import statistics
import unicodedata
import urllib.parse
import urllib.request

from bm25_baseline import bm25_scores, words
from compare_checkpoints import atomic_report

SOURCE = "miracl/miracl"
API = "https://datasets-server.huggingface.co/rows"
HF_API = "https://huggingface.co/api/datasets/miracl/miracl"
LANGUAGES = ("ru", "en")
FORMAT = "posta-miracl-human-dev-judged-pool-v1"


def get_json(url, limit=15000000):
    request = urllib.request.Request(
        url, headers={"User-Agent": "Posta-Multi-Eval/1.0",
                      "Accept": "application/json"})
    with urllib.request.urlopen(request, timeout=40) as stream:
        if int(stream.headers.get("Content-Length") or 0) > limit:
            raise ValueError("source response exceeds 15MB page cap")
        raw = stream.read(limit + 1)
    if len(raw) > limit:
        raise ValueError("source response exceeds 15MB page cap")
    return json.loads(raw)


def revision(fetch):
    record = fetch(HF_API)
    sha = record.get("sha")
    if (not isinstance(sha, str) or len(sha) != 40 or
            any(c not in "0123456789abcdef" for c in sha)):
        raise ValueError("missing MIRACL source revision")
    if record.get("private") or record.get("gated") not in (None, False):
        raise ValueError("MIRACL source is not openly accessible")
    return sha


def download_dev(lang, fetch=get_json, page_size=100):
    if lang not in LANGUAGES or not 1 <= page_size <= 100:
        raise ValueError("unrecognized language or page size")
    records, offset, total = [], 0, None
    while total is None or offset < total:
        query = urllib.parse.urlencode(dict(dataset=SOURCE, config=lang,
                                            split="dev", offset=offset,
                                            length=page_size))
        page = fetch(API + "?" + query)
        total = page.get("num_rows_total")
        if type(total) is not int or not 1 <= total <= 1500:
            raise ValueError("MIRACL dev row count outside verified 1500 cap")
        entries = page.get("rows")
        expected = min(page_size, total-offset)
        if not isinstance(entries, list) or len(entries) != expected:
            raise ValueError("incomplete MIRACL dev dataset server page")
        for entry in entries:
            if not isinstance(entry, dict) or not isinstance(entry.get("row"), dict):
                raise ValueError("invalid MIRACL row")
            records.append(entry["row"])
        offset += expected
    return records


def normalize(value):
    return " ".join(unicodedata.normalize("NFKC", value).casefold().split())


def checked_row(row, lang):
    query, query_id = row.get("query"), row.get("query_id")
    if (not isinstance(query, str) or len(query.strip()) < 3 or
            not isinstance(query_id, str) or not query_id.strip()):
        return None
    documents = {}
    for label, name in ((1, "positive_passages"), (0, "negative_passages")):
        source = row.get(name)
        if not isinstance(source, list):
            return None
        for passage in source:
            if not isinstance(passage, dict):
                return None
            docid, text, title = (passage.get("docid"),
                                  passage.get("text"), passage.get("title"))
            if (not isinstance(docid, str) or not docid or
                    not isinstance(text, str) or not text.strip() or
                    not isinstance(title, str)):
                return None
            combined = (title.strip() + " " + text.strip()).strip()
            if not 10 <= len(combined) <= 30000:
                return None
            item = {"id": docid, "text": combined, "relevant": label}
            if docid in documents and documents[docid] != item:
                return None
            documents[docid] = item
    items = sorted(documents.values(), key=lambda d: d["id"])
    if not any(d["relevant"] for d in items) or not any(not d["relevant"] for d in items):
        return None
    fingerprint = {}
    for item in items:
        key = normalize(item["text"])
        if key in fingerprint and fingerprint[key] != item["relevant"]:
            return None
        fingerprint[key] = item["relevant"]
    return {"id": lang + ":" + query_id, "language": lang,
            "query": query.strip(), "documents": items}


def freeze(rows, lang, count=200):
    if type(count) is not int or count < 1:
        raise ValueError("invalid query count")
    eligible = {}
    for row in rows:
        item = checked_row(row, lang)
        if item is None:
            continue
        if item["id"] in eligible and item != eligible[item["id"]]:
            raise ValueError("conflicting query id in MIRACL")
        eligible[item["id"]] = item
    if len(eligible) < count:
        raise ValueError(f"MIRACL {lang} only has {len(eligible)} usable "
                         f"annotated queries; expected {count}")
    keys = sorted(eligible, key=lambda key: (
        hashlib.sha256(key.encode()).digest(), key))[:count]
    return [eligible[key] for key in keys], {
        "rows_read": len(rows), "usable_queries": len(eligible), "selected": count,
    }


def query_scores(item):
    docs = item["documents"]
    scores = bm25_scores(item["query"], [words(doc["text"]) for doc in docs])
    positive = [i for i, doc in enumerate(docs) if doc["relevant"]]
    negative = [i for i, doc in enumerate(docs) if not doc["relevant"]]
    ranked = sorted(range(len(docs)), key=lambda i: (-scores[i], docs[i]["id"]))
    first = min(ranked.index(i)+1 for i in positive)
    dcg = sum(1/math.log2(rank+2) for rank, i in enumerate(ranked[:10])
              if i in positive)
    ideal = sum(1/math.log2(rank+2)
                for rank in range(min(10, len(positive))))
    return dict(recall_1=float(first == 1), recall_5=float(first <= 5),
                mrr=1/first, ndcg_10=dcg/ideal,
                pair_accuracy=sum(scores[p] > scores[n]
                                  for p in positive for n in negative)
                              / (len(positive)*len(negative)),
                documents=len(docs), positives=len(positive))


def summarize(items, bootstrap=400):
    if not items:
        raise ValueError("no judgments to summarize")
    rng = random.Random(20261121)
    metrics = ("recall_1", "recall_5", "mrr", "ndcg_10", "pair_accuracy")
    result = {"queries": len(items),
              "mean_judged_documents": round(statistics.mean(
                  x["documents"] for x in items), 2),
              "mean_positive_documents": round(statistics.mean(
                  x["positives"] for x in items), 2)}
    ci = {}
    for metric in metrics:
        values = [entry[metric] for entry in items]
        result[metric] = round(statistics.mean(values), 6)
        samples = sorted(sum(values[rng.randrange(len(values))]
                             for _ in range(len(values))) / len(values)
                         for _ in range(bootstrap))
        ci[metric] = [round(samples[int(.025*(bootstrap-1))], 6),
                      round(samples[int(.975*(bootstrap-1))], 6)]
    result["bootstrap_95pct"] = ci
    return result


def evaluate(groups, sha, source_counts):
    if not isinstance(sha, str) or len(sha) != 40 or set(groups) != set(LANGUAGES):
        raise ValueError("invalid source or language selection")
    metric_rows = {lang: [query_scores(r) for r in groups[lang]]
                   for lang in LANGUAGES}
    digest = hashlib.sha256()
    for lang in LANGUAGES:
        for row in groups[lang]:
            encoded = json.dumps(row, ensure_ascii=False,
                                 separators=(",", ":")).encode()
            digest.update(len(encoded).to_bytes(8, "big"))
            digest.update(encoded)
    return {"format": FORMAT, "source": SOURCE, "source_split": "dev",
            "source_revision_observed": sha, "frozen_data_sha256": digest.hexdigest(),
            "annotation": "human-judged (MIRACL)",
            "languages": {lang: summarize(rows) for lang, rows in metric_rows.items()},
            "overall": summarize([item for rows in metric_rows.values()
                                  for item in rows]),
            "source_counts": source_counts,
            "limitations": [
                "Judged-pool reranking only, NOT full-corpus retrieval",
                "No semantic or multimodal evaluation of actual Posta posts",
                "MIRACL must never be used for training or tokenizer fitting",
                "HF Dataset Server pages are not revision-pinned",
                "Source HEAD was checked before and after download",
                "No raw text is emitted into aggregate JSON or logs",
            ]}


def save_private(path, groups):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as stream:
        for lang in LANGUAGES:
            for row in groups[lang]:
                stream.write(json.dumps(row, ensure_ascii=False)+"\n")


def run(report_file, data_file, count=200, fetch=get_json):
    report_file, data_file = Path(report_file), Path(data_file)
    if report_file.exists() or data_file.exists():
        raise FileExistsError("MIRACL frozen dataset already exists")
    before = revision(fetch)
    groups, counts = {}, {}
    for language in LANGUAGES:
        records = download_dev(language, fetch)
        groups[language], counts[language] = freeze(records, language, count)
    if revision(fetch) != before:
        raise ValueError("MIRACL source revision changed during download")
    report = evaluate(groups, before, counts)
    save_private(data_file, groups)
    try:
        atomic_report(report_file, report)
    except Exception:
        data_file.unlink(missing_ok=True)
        raise
    return report


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--data-file", type=Path, required=True)
    p.add_argument("--queries-per-language", type=int, default=200)
    args = p.parse_args()
    result = run(args.output, args.data_file, args.queries_per_language)
    print(json.dumps({"dataset": FORMAT,
                      "queries": result["overall"]["queries"],
                      "source_revision": result["source_revision_observed"],
                      "sha256": result["frozen_data_sha256"],
                      "bm25_ndcg_10": result["overall"]["ndcg_10"],
                      "bm25_mrr": result["overall"]["mrr"]}))


if __name__ == "__main__":
    main()
