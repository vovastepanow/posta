"""Read-only, full-supplied-corpus NanoMIRACL evaluation of a Run 001 best retriever.

The private frozen snapshot is evaluation-only and is never downloaded again,
retrained on, uploaded, or exported. The baseline must contain the exact frozen
file SHA, not merely a claimed dataset revision. Requires a *trained* Run 001
stage-2 best checkpoint. This never creates or modifies model weights.
"""
import argparse
import hashlib
import json
import math
from pathlib import Path

from nanomiracl_benchmark import LANGUAGES, SOURCE, summarize
from compare_checkpoints import atomic_report

FORMAT = "posta-run001-frozen-nanomiracl-model-eval-v1"
FROZEN_FORMAT = "posta-nanomiracl-frozen-evaluation-v1"
BM25_FORMAT = "posta-nanomiracl-ru-en-full-nano-corpus-bm25-v1"
CHECKPOINT_FORMAT = "posta-multi-235m-run001-retrieval-v1-best"
MAX_SNAPSHOT_BYTES = 32 * 1024 * 1024


def logical_sha(groups):
    """Match the exact canonical digest used by nanomiracl_benchmark.prepare."""
    digest = hashlib.sha256()
    for lang in LANGUAGES:
        raw = json.dumps(groups[lang], ensure_ascii=False, sort_keys=True,
                         separators=(",", ":")).encode("utf-8")
        digest.update(len(raw).to_bytes(8, "big"))
        digest.update(raw)
    return digest.hexdigest()


def load_frozen(snapshot, baseline):
    """Verify exact private bytes, logical dataset and qrel/ID integrity.

    This runs BEFORE any expensive GPU checkpoint load.
    """
    snapshot, baseline = Path(snapshot), Path(baseline)
    length = snapshot.stat().st_size
    if not 0 < length <= MAX_SNAPSHOT_BYTES:
        raise ValueError("frozen snapshot exceeds bounded evaluation budget")
    raw = snapshot.read_bytes()
    report = json.loads(baseline.read_text(encoding="utf-8"))
    actual_file_sha = hashlib.sha256(raw).hexdigest()
    if report.get("format") != BM25_FORMAT or not report.get("evaluation_only"):
        raise ValueError("incorrect frozen BM25 baseline format")
    if report.get("frozen_file_sha256") != actual_file_sha:
        raise ValueError("snapshot does not match SHA-pinned frozen BM25 baseline")
    payload = json.loads(raw)
    if payload.get("format") != FROZEN_FORMAT or payload.get("source") != SOURCE:
        raise ValueError("unexpected frozen dataset or format")
    groups = payload.get("languages")
    if not isinstance(groups, dict) or set(groups) != set(LANGUAGES):
        raise ValueError("frozen languages differ from baseline")
    if (payload.get("source_content_sha256") != report.get("source_content_sha256")
            or logical_sha(groups) != report["source_content_sha256"]):
        raise ValueError("frozen logical source digest differs from baseline")
    if payload.get("source_revision_observed") != report.get("source_revision_observed"):
        raise ValueError("frozen source revision differs from baseline")

    for lang in LANGUAGES:
        group = groups[lang]
        if not isinstance(group, dict) or set(group) != {"queries", "corpus", "qrels"}:
            raise ValueError("invalid frozen language group")
        queries, docs, qrels = (group[name] for name in ("queries", "corpus", "qrels"))
        if not all(isinstance(x, dict) for x in (queries, docs, qrels)):
            raise ValueError("frozen queries, corpus and qrels must be mappings")
        if len(queries) != 200 or set(queries) != set(qrels) or not 400 <= len(docs) <= 10000:
            raise ValueError("frozen query/document/qrel counts are invalid")
        expected = report.get("candidate_corpora", {}).get(lang, {})
        if (expected.get("queries") != len(queries) or
                expected.get("documents") != len(docs)):
            raise ValueError("frozen corpus sizes differ from baseline")
        observed_qrels = 0
        for mapping in (queries, docs):
            if any(not isinstance(key, str) or not key or
                   not isinstance(value, str) or not value.strip()
                   for key, value in mapping.items()):
                raise ValueError("invalid frozen query or document")
        for query_id, positive_ids in qrels.items():
            if (not isinstance(positive_ids, list) or not positive_ids or
                    len(positive_ids) != len(set(positive_ids)) or
                    any(not isinstance(doc_id, str) or doc_id not in docs
                        for doc_id in positive_ids)):
                raise ValueError("invalid positive relevance identifiers")
            observed_qrels += len(positive_ids)
        if expected.get("qrels") != observed_qrels:
            raise ValueError("frozen relevance count differs from baseline")
    return groups, report, actual_file_sha


def score_ranking(ids, similarities, relevant):
    """Document-id deterministic ties. Hits and true graded positive recall differ."""
    if not ids or len(ids) != len(similarities) or not relevant:
        raise ValueError("missing ranked candidates or relevance labels")
    if not set(relevant).issubset(ids):
        raise ValueError("known positive absent from candidate pool")
    values = [float(x) for x in similarities]
    if not all(math.isfinite(value) for value in values):
        raise ValueError("non-finite similarity")
    ordered = sorted(range(len(ids)), key=lambda i: (-values[i], ids[i]))
    ranks = [rank for rank, i in enumerate(ordered, 1) if ids[i] in relevant]
    first = min(ranks)
    dcg = sum(1.0 / math.log2(rank + 1) for rank in ranks if rank <= 10)
    ideal = sum(1.0 / math.log2(rank + 2) for rank in range(min(10, len(relevant))))
    outcome = {"mrr": 1.0 / first, "ndcg_10": dcg / ideal}
    for k in (1, 5, 10):
        outcome[f"hit_{k}"] = float(first <= k)
        outcome[f"positive_recall_{k}"] = sum(rank <= k for rank in ranks) / len(relevant)
    return outcome


def summarize_scores(rows):
    keys = ("hit_1", "hit_5", "hit_10", "positive_recall_1",
            "positive_recall_5", "positive_recall_10", "mrr", "ndcg_10")
    return {"queries": len(rows), **{
        key: round(sum(row[key] for row in rows) / len(rows), 6) for key in keys}}


def evaluate_groups(groups, embed):
    """Embed every distinct text once, keep corpus/query vectors on CPU."""
    import torch
    results = {}
    for lang in LANGUAGES:
        group = groups[lang]
        docs = group["corpus"]
        ids = sorted(docs)
        corpus_vectors = torch.stack([embed(docs[doc_id]).float().cpu() for doc_id in ids])
        if corpus_vectors.ndim != 2 or corpus_vectors.shape[1] != 512:
            raise ValueError("expected 512-dimensional corpus embeddings")
        if not torch.isfinite(corpus_vectors).all():
            raise ValueError("non-finite candidate embedding")
        rows = []
        for qid in sorted(group["queries"]):
            query = embed(group["queries"][qid]).float().cpu()
            if query.shape != (512,) or not torch.isfinite(query).all():
                raise ValueError("invalid 512-dimensional query embedding")
            scores = torch.mv(corpus_vectors, query).tolist()
            rows.append(score_ranking(ids, scores, set(group["qrels"][qid])))
        results[lang] = rows
    return {
        "languages": {lang: summarize_scores(results[lang]) for lang in LANGUAGES},
        "overall": summarize_scores([v for lang in LANGUAGES for v in results[lang]]),
    }


def evaluate(snapshot, baseline, checkpoint, tokenizer_path, output):
    import torch
    from tokenizers import Tokenizer
    from model import PostaMulti
    from spec import Config, parameter_counts
    from run001_smoke import RUN_ID

    output = Path(output)
    if output.exists():
        raise FileExistsError("refuse to overwrite an existing evaluation")
    groups, report, file_sha = load_frozen(snapshot, baseline)
    if not torch.cuda.is_available():
        raise RuntimeError("235M frozen evaluation requires CUDA")
    state = torch.load(checkpoint, map_location="cpu", weights_only=True)
    settings = state.get("settings", {})
    tokenizer_sha = hashlib.sha256(Path(tokenizer_path).read_bytes()).hexdigest()
    if (state.get("format") != CHECKPOINT_FORMAT or
            settings.get("format") != CHECKPOINT_FORMAT[:-5] or
            settings.get("run_id") != RUN_ID or
            settings.get("tokenizer_sha256") != tokenizer_sha or
            not isinstance(state.get("step"), int) or state["step"] < 1):
        raise ValueError("not a matching trained Run 001 retrieval best checkpoint")
    config = Config(**settings["config"])
    if (config.text_pooling != "mean-residual" or
            parameter_counts(config)["total"] != 235179777):
        raise ValueError("235M retrieval configuration mismatch")
    max_length = settings["max_length"]
    if not isinstance(max_length, int) or not 8 <= max_length <= config.text_length:
        raise ValueError("invalid checkpoint text token length")
    tokenizer = Tokenizer.from_file(str(tokenizer_path))
    for token, expected in (("[PAD]", 0), ("[CLS]", 1), ("[SEP]", 2)):
        if tokenizer.token_to_id(token) != expected:
            raise ValueError("checkpoint tokenizer special IDs mismatch")
    tokenizer.enable_truncation(max_length=max_length)
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    state_step = state["step"]
    model = PostaMulti(config, dropout=0).half().cuda()
    model.load_state_dict(state["model"])
    model.eval()
    del state

    @torch.no_grad()
    def embed(value):
        ids = tokenizer.encode(value).ids
        if len(ids) < 3 or ids[0] != 1 or ids[-1] != 2:
            raise ValueError("invalid query/document tokenization")
        tokens = torch.tensor([ids], device="cuda", dtype=torch.long)
        return model.encode_text(tokens, torch.ones_like(tokens, dtype=torch.bool))[0]

    scores = evaluate_groups(groups, embed)
    result = {
        "format": FORMAT, "run_id": RUN_ID,
        "checkpoint_step": state_step, "checkpoint_sha256": file_digest(checkpoint),
        "tokenizer_sha256": tokenizer_sha,
        "source_content_sha256": report["source_content_sha256"],
        "frozen_file_sha256": file_sha,
        "source_revision_observed": report["source_revision_observed"],
        "candidate_corpora": report["candidate_corpora"],
        "candidate_pool": "entire frozen provided NanoMIRACL corpus per language",
        "evaluation_only": True, "trained_on_evaluation_data": False,
        "model": "Posta Multi 235M / Run 001 stage 2 retrieval best",
        "metrics": scores,
        "baseline_bm25": {
            lang: {k: report["languages"][lang][k]
                   for k in ("queries", "recall_1", "recall_5", "recall_10", "mrr", "ndcg_10")}
            for lang in LANGUAGES
        },
        "baseline_note": "BM25 recall_1/5/10 fields are hit@K (any known positive), not fraction-of-positives recall",
        "limitations": [
            "Frozen NanoMIRACL subset, not official MIRACL dev or full MIRACL corpus",
            "Human-derived qrels are known positives; unjudged texts may be relevant",
            "No post/multimodal evaluation or production promotion",
            "Model and BM25 use the same frozen query/corpus and full candidate pool",
        ],
    }
    atomic_report(output, result)
    print(json.dumps({
        "run_id": RUN_ID, "checkpoint_step": state_step,
        "source_content_sha256": result["source_content_sha256"],
        "overall": scores["overall"], "languages": scores["languages"],
    }))


def file_digest(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--frozen", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, required=True,
                        help="exact BM25 report with frozen_file_sha256")
    parser.add_argument("--checkpoint", type=Path, required=True,
                        help="trained run001-retrieval-best.pt")
    parser.add_argument("--tokenizer", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    evaluate(args.frozen, args.baseline, args.checkpoint, args.tokenizer, args.output)


if __name__ == "__main__":
    main()
