"""Full-candidate BM25 evaluation on NanoMIRACL RU/EN, no model or GPU.

Each language searches its ENTIRE supplied NanoMIRACL corpus; never use
published 'bm25' candidate lists because some relevant docs were forcibly
inserted into them. Qrels annotate known positives only. Unjudged docs
are NOT assumed to be verified negatives. Dataset is derived from
human-labeled MIRACL; the Nano selection is not official MIRACL dev.

Small public evaluation-only data, fetched from Hugging Face Dataset
Server. Repo HEAD checked before/after; actual content SHA is frozen.
No queries or passage text are written to reports or logs.
"""
import argparse
from collections import Counter, defaultdict
import hashlib
import json
import math
import os
from pathlib import Path
import random
import statistics
import urllib.parse
import urllib.request

from bm25_baseline import words
from compare_checkpoints import atomic_report

SOURCE = "hotchpotch/NanoMIRACL"
SPLITS_API = "https://datasets-server.huggingface.co/splits"
ROWS_API = "https://datasets-server.huggingface.co/rows"
META_API = "https://huggingface.co/api/datasets/hotchpotch/NanoMIRACL"
LANGUAGES = ("ru", "en")
CONFIGS = ("queries", "corpus", "qrels")
FORMAT = "posta-nanomiracl-ru-en-full-nano-corpus-bm25-v1"


def fetch_json(url, *, limit=16000000):
    req = urllib.request.Request(
        url, headers={"Accept": "application/json",
                      "User-Agent": "Posta-235M-evaluation/1.0"})
    with urllib.request.urlopen(req, timeout=45) as stream:
        if int(stream.headers.get("Content-Length") or 0) > limit:
            raise ValueError("response exceeds bounded evaluation budget")
        payload = stream.read(limit + 1)
    if len(payload) > limit:
        raise ValueError("response exceeds bounded evaluation budget")
    return json.loads(payload)


def source_sha(fetch):
    info = fetch(META_API)
    sha = info.get("sha")
    if (not isinstance(sha, str) or len(sha) != 40 or
            any(char not in "0123456789abcdef" for char in sha)):
        raise ValueError("source commit SHA missing")
    if info.get("private") or info.get("gated") not in (None, False):
        raise ValueError("dataset is gated/private")
    return sha


def available_splits(fetch):
    response = fetch(SPLITS_API + "?" + urllib.parse.urlencode({"dataset": SOURCE}))
    found = {}
    for entry in response.get("splits", []):
        config, split = entry.get("config"), entry.get("split")
        if config in CONFIGS and split in LANGUAGES:
            # HF Dataset Server /splits may return only dataset/config/split.
            # Determine the actual row count from the first /rows response.
            count = entry.get("num_examples")
            if count is not None and (type(count) is not int or count < 1):
                raise ValueError("invalid optional split example count")
            found[(config, split)] = count
    if set(found) != {(c, lang) for c in CONFIGS for lang in LANGUAGES}:
        raise ValueError("NanoMIRACL published configs or split names changed")
    for lang in LANGUAGES:
        for config, lower, upper in (("queries", 100, 300),
                                     ("qrels", 100, 1200),
                                     ("corpus", 400, 10000)):
            value = found[(config, lang)]
            if value is not None and not lower <= value <= upper:
                raise ValueError("source metadata count outside Nano budget")
    return found


def rows(config, lang, count, fetch, *, page_size=100):
    if config not in CONFIGS or lang not in LANGUAGES:
        raise ValueError("invalid source selection")
    results, total, offset = [], count, 0
    limits = {"queries": (100, 300), "qrels": (100, 1200),
              "corpus": (400, 10000)}
    while total is None or offset < total:
        length = page_size if total is None else min(page_size, total-offset)
        url = ROWS_API + "?" + urllib.parse.urlencode({
            "dataset": SOURCE, "config": config, "split": lang,
            "offset": offset, "length": length})
        page = fetch(url)
        reported = page.get("num_rows_total")
        if type(reported) is not int:
            raise ValueError("Nano /rows did not provide num_rows_total")
        lo, hi = limits[config]
        if not lo <= reported <= hi or (total is not None and reported != total):
            raise ValueError("Nano row count changed or outside bounded budget")
        total = reported
        entries = page.get("rows")
        expected = min(length, total-offset)
        if not isinstance(entries, list) or len(entries) != expected:
            raise ValueError("missing or inconsistent NanoMIRACL Dataset Server page")
        for entry in entries:
            if not isinstance(entry, dict) or not isinstance(entry.get("row"), dict):
                raise ValueError("invalid NanoMIRACL row")
            results.append(entry["row"])
        offset += expected
    return results


def validated_language(queries, corpus, qrels, lang):
    q, docs, positives = {}, {}, defaultdict(set)
    for entry in queries:
        key, value = entry.get("_id"), entry.get("text")
        if not isinstance(key, str) or not key or \
           not isinstance(value, str) or not value.strip() or key in q:
            raise ValueError("invalid or duplicate Nano query")
        q[key] = value
    for entry in corpus:
        key, value = entry.get("_id"), entry.get("text")
        if not isinstance(key, str) or not key or \
           not isinstance(value, str) or not value.strip() or key in docs:
            raise ValueError("invalid or duplicate Nano document")
        docs[key] = value
    for entry in qrels:
        query_id, doc_id = entry.get("query-id"), entry.get("corpus-id")
        if query_id not in q or doc_id not in docs:
            raise ValueError("qrel references missing query/document")
        positives[query_id].add(doc_id)
    if set(q) != set(positives):
        raise ValueError("not all Nano queries have positive human-derived qrels")
    if len(q) != 200:
        raise ValueError(f"expected 200 Nano {lang} queries, got {len(q)}")
    return q, docs, dict(positives)


class BM25Index:
    """Inverted-index BM25 over all Nano documents, unjudged docs retained."""
    def __init__(self, docs, k1=1.2, b=0.75):
        if not docs:
            raise ValueError("empty candidate corpus")
        self.ids = sorted(docs)
        self.lengths = {}
        self.postings = defaultdict(list)
        for index, doc_id in enumerate(self.ids):
            counts = Counter(words(docs[doc_id]))
            self.lengths[index] = sum(counts.values())
            for token, frequency in counts.items():
                self.postings[token].append((index, frequency))
        self.n = len(self.ids)
        self.k1, self.b = k1, b
        self.avg = sum(self.lengths.values()) / self.n

    def search(self, query):
        scores = [0.0] * self.n
        if self.avg == 0:
            return sorted(range(self.n), key=lambda i: self.ids[i])
        for word, count in Counter(words(query)).items():
            posting = self.postings.get(word)
            if not posting:
                continue
            df = len(posting)
            idf = math.log1p((self.n - df + 0.5)/(df + 0.5))
            for index, frequency in posting:
                denominator = frequency + self.k1 * (
                    1-self.b + self.b*self.lengths[index]/self.avg)
                scores[index] += count * idf * frequency*(self.k1+1)/denominator
        return sorted(range(self.n), key=lambda i: (-scores[i], self.ids[i]))


def evaluate_language(queries, docs, qrels):
    index = BM25Index(docs)
    stats = []
    for query_id in sorted(queries):
        ranked = index.search(queries[query_id])
        rank_ids = [index.ids[i] for i in ranked]
        positives = qrels[query_id]
        ranks = [r+1 for r, doc_id in enumerate(rank_ids) if doc_id in positives]
        best = min(ranks)
        dcg = sum(1.0/math.log2(r+1) for r in ranks if r <= 10)
        ideal = sum(1.0/math.log2(r+2) for r in range(min(len(positives), 10)))
        stats.append({
            "recall_1": float(best <= 1),
            "recall_5": float(best <= 5),
            "recall_10": float(best <= 10),
            "mrr": 1.0/best,
            "ndcg_10": dcg/ideal,
        })
    return stats


def summarize(items):
    rng = random.Random(20261121)
    keys = ("recall_1", "recall_5", "recall_10", "mrr", "ndcg_10")
    result = {"queries": len(items)}
    for key in keys:
        values = [item[key] for item in items]
        result[key] = round(statistics.mean(values), 6)
    return result


def prepare(fetch=fetch_json, freeze_data=None):
    revision = source_sha(fetch)
    sizes = available_splits(fetch)
    grouped, per_language, counts = {}, {}, {}
    for lang in LANGUAGES:
        q = rows("queries", lang, sizes[("queries", lang)], fetch)
        c = rows("corpus", lang, sizes[("corpus", lang)], fetch)
        r = rows("qrels", lang, sizes[("qrels", lang)], fetch)
        queries, docs, qrels = validated_language(q, c, r, lang)
        per_language[lang] = evaluate_language(queries, docs, qrels)
        counts[lang] = {"queries":len(queries), "documents":len(docs),
                        "qrels":sum(len(v) for v in qrels.values())}
        grouped[lang] = {"queries": queries, "corpus": docs,
                         "qrels": {k:sorted(v) for k,v in qrels.items()}}
    if revision != source_sha(fetch):
        raise ValueError("source HEAD changed mid-evaluation")
    h = hashlib.sha256()
    for lang in LANGUAGES:
        raw = json.dumps(grouped[lang], ensure_ascii=False,
                         sort_keys=True, separators=(",", ":")).encode()
        h.update(len(raw).to_bytes(8,"big"))
        h.update(raw)
    report = {
        "format": FORMAT, "source": SOURCE, "source_revision_observed": revision,
        "query_selection": "NanoMIRACL publisher-selected (testB when available)",
        "source_content_sha256": h.hexdigest(), "candidate_corpora": counts,
        "languages": {lang: summarize(metrics) for lang, metrics in per_language.items()},
        "overall": summarize([s for lang in LANGUAGES for s in per_language[lang]]),
        "candidate_pool": "entire provided NanoMIRACL per-language corpus",
        "source_bm25_candidates_used": False,
        "evaluation_only": True,
        "limitations": [
            "NanoMIRACL derived from MIRACL, not official MIRACL dev",
            "Source query subset preferentially uses MIRACL testB",
            "Qrels mark known positives, unjudged documents are not verified negatives",
            "Full Nano 1-2K candidate pool, not multi-million MIRACL corpus",
            "Source BM25 candidate lists are NOT used (they inject positives)",
            "No real Posta posts or multimodal relevance is measured",
            "HF Dataset Server cache cannot be SHA-pinned; content SHA frozen",
            "No raw query/document text published with aggregate metrics",
        ],
    }
    if freeze_data is not None:
        # Preserve source contents for all later model comparisons. Do not
        # refetch or silently substitute new dataset-server revisions.
        destination = Path(freeze_data)
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.exists():
            raise FileExistsError("Nano snapshot exists; never overwrite the frozen evaluation")
        payload = json.dumps(
            {"format": "posta-nanomiracl-frozen-evaluation-v1",
             "source": SOURCE, "source_revision_observed": revision,
             "source_content_sha256": report["source_content_sha256"],
             "languages": grouped},
            ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode()
        import os
        handle = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        try:
            with os.fdopen(handle, "wb") as stream:
                stream.write(payload)
                stream.flush()
                os.fsync(stream.fileno())
        except BaseException:
            destination.unlink(missing_ok=True)
            raise
        report["frozen_file_sha256"] = hashlib.sha256(payload).hexdigest()
        report["frozen_file_bytes"] = len(payload)
        report["frozen_file_is_local_only"] = True
    return report


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--output", type=Path, required=True)
    p.add_argument("--freeze-data", type=Path,
                   help="local-only evaluation snapshot, mode 0600; never upload")
    args = p.parse_args()
    if args.freeze_data is not None and args.freeze_data.exists():
        raise FileExistsError("frozen evaluation snapshot already exists")
    if args.output.exists():
        raise FileExistsError("frozen Nano benchmark report already exists")
    result = prepare(freeze_data=args.freeze_data)
    atomic_report(args.output, result)
    print(json.dumps({
        "dataset": SOURCE, "source_revision": result["source_revision_observed"],
        "frozen_sha": result["source_content_sha256"],
        "ru": result["languages"]["ru"], "en":result["languages"]["en"],
        "candidate_corpora": result["candidate_corpora"]
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
