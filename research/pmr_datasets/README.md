# Posta Multi 235M (Поста Мульти 235М) — dataset audit v0.1 (research only)

This directory is **not** a replacement for Posta's production `cmd/posta-ml` classifier, server, search, or DB.
All commands are stdlib-only Python 3 scripts. No automatic downloads, training or migrations.

## Scope and invariants (20 Nov 2026)

- **A real, self-trained retriever** means randomly initialized core weights. Hugging Face embeddings in existing datasets (e.g. FineWeb2-HQ) are **not** features to paste into PMR at inference.
- The catalog represents **candidates**, not a cleared/licensed or locally available training corpus.
- Russian and English are primary pilot languages; language selection for other languages must be explicit.
- Text pretraining, ranking supervision, image alignment, OCR, video alignment, speech and independent evaluation are **different data tasks**.
- Model architecture and database layout stay unchanged pending actual schema sampling.
- A remote dataset card's license tag does not clear the rights of each underlying picture/audio/video.
- Never download a multi-GB tar/HDF5 shard just to determine the schema; first review file lists and plan disk use.
- **No MMEB-V3 evaluation rows in training**, nor other benchmark held-out splits.
- Human approval of a specific upstream dataset revision and provenance is required by `prepare.py`.

## Verified observations from published dataset cards (not local byte-level checks)

| Dataset | Critical limitation |
| --- | --- |
| FineWeb2 Russian | `rus_Cyrl` exists; official reported full compressed size ~1.81 TB |
| FineWeb2-HQ Russian | `rus_Cyrl` exists; official reported size ~1.2 TB, contains large precomputed embeddings; overlaps FineWeb2 |
| KaLM-embedding-finetuning-data | HF viewer has `query`, `pos[]`, `neg[]`; English/Chinese listed, not a Russian replacement |
| BGE-M3 data | HF preview exposes `test` (~172k rows); cannot treat it as a reviewed training split |
| WIT `wit_base` | Preview only first 5 GB (~108k visible rows), not proof that the underlying full corpus contains only 108k |
| MegaPairs | Access requires accepting conditions on Hugging Face; claimed full size ~775 GB |
| NVIDIA OCR Synthetic Multilingual v1 | HDF5; HF viewer not working; requires a separate small-file/manual shard inspection |
| VideoUFO | Annotations & preview include middle frame; full videos in separate large tar archives (~800 GB) |
| Official Common Voice HF `common_voice_17_0` | Empty since releases moved to Mozilla Data Collective in Oct 2025; **blocked** for automatic downloads |
| MMEB-V3 | Independent evaluation only, no direct training |

Cards/references:
- https://huggingface.co/datasets/HuggingFaceFW/fineweb-2
- https://huggingface.co/datasets/epfml/FineWeb2-HQ
- https://huggingface.co/datasets/KaLM-Embedding/KaLM-embedding-finetuning-data
- https://huggingface.co/datasets/Shitao/bge-m3-data
- https://huggingface.co/datasets/wikimedia/wit_base
- https://huggingface.co/datasets/JUNJIE99/MegaPairs
- https://huggingface.co/datasets/nvidia/OCR-Synthetic-Multilingual-v1
- https://huggingface.co/datasets/WenhaoWang/VideoUFO
- https://huggingface.co/datasets/mozilla-foundation/common_voice_17_0
- https://huggingface.co/datasets/VLM2Vec/MMEB-V3


## Model naming and verified metadata snapshot (21 Nov 2026)

The target product model is named **Posta Multi 235M (Поста Мульти 235М)**.
`PMR` remains an internal shorthand for the Posta Multimodal Retriever research pipeline.
The **235M** number is a target size, not an already trained or frozen architecture.

An isolated GitHub Actions run audited six publicly accessible Hugging Face metadata
endpoints on **2026-11-21 at 13:50 UTC**. Both the bounded metadata workflow and
the production Go test workflow succeeded at commit `f3ad583e`.

| Catalog ID | Audited revision SHA | Previewed config / split | Schema |
| --- | --- | --- | --- |
| `fineweb2-ru` | `af9c13333eb981300149d5ca60a8e9d659b276b9` | `rus_Cyrl / train` | text, id, language, url, quality metadata |
| `fineweb-en` | `9bb295ddab0e05d785b879661af7260fed5140fc` | `default / train` | text, id, language, token_count, url |
| `kalm-retrieval` | `e9443ab6f5d4dc29c79cea03834e932428ed6ab1` | `default / train` | query, pos[], neg[] |
| `wit` | `ff6d4fb32fd566d3a1fa20e946cba3234179465e` | `default / train` | image, image_url, caption metadata, embeddings |
| `videoufo` | `683425832ad5b35d57d213963ecec18d5b689652` | `default / Full` | captions, middle frame, timestamps, quality scores |
| `memexplain` | `2d08e071512ad6d6ad40bb18f1f793124ca25968` | `HatefulMemeXplain / train` | image, text, explanation, annotations |

Source: [GitHub Actions metadata inventory run](https://github.com/vovastepanow/posta/actions/runs/44910807628).
These **observed HEAD revisions and preview schemas are not approvals to train**.
No full text corpus, media bytes, or per-asset rights were validated.
Pinned revision-aware pilot exports require the separate human-reviewed approval gate.

## Step 1: Read-only metadata/schema probe

```bash
python3 research/pmr_datasets/probe.py --out data/pmr/audit.json
python3 research/pmr_datasets/probe.py --ids fineweb2-ru kalm-retrieval wit memexplain --rows --out data/pmr/audit-rows.json
```

The audit uses bounded HF API requests and stores **metadata, revision SHAs and schema column types only**, **never full raw examples/media**. Very large repository file lists are deliberately not fetched; exact shard sizes require a separate inspection.
The HF Dataset Server cannot preview some huge/gated/HDF5/WebDataset sources; a failure is logged, not fabricated.
`--strict` exits nonzero if a check fails. HTTP timeouts/rate limits are distinct from license denial.

## Optional: small text export after review

Only after the approval gate below is genuinely satisfied, run the bounded text exporter.
It uses HF Dataset Server `/rows`, does not download Parquet, and checks that the
source revision SHA has not changed during export. Do not interpret the deterministic
offset sampling as statistically representative of the entire corpus.

```bash
python3 research/pmr_datasets/export_text.py --id fineweb2-ru --count 20 \\
  --audit data/pmr/audit.json --approvals data/pmr/approvals.json
python3 research/pmr_datasets/export_text.py --id fineweb-en --count 20 \\
  --audit data/pmr/audit.json --approvals data/pmr/approvals.json
```

The outputs appear in ignored `data/pmr/*-sample.jsonl` and can then be passed to
`prepare.py`. This exports **only text**, not media; quality and personal-information
screening are still required. A failed request does not produce a partial output.

## Step 2: Human review and pilot input

After inspecting upstream terms and the actual media rights, create **your own** `data/pmr/approvals.json`. This is *an example shape*, not granted approval:

```json
{
  "fineweb2-ru": {
    "approved_for_training": false,
    "revision": "paste SHA from data/pmr/audit.json",
    "review_note": "describe source terms, media rights if relevant, and any exclusions"
  }
}
```

Exported input to `prepare.py` must be independent pre-reviewed **JSONL**, e.g.:

```json
{"dataset_id":"fineweb2-ru","record_id":"uuid-or-upstream-id","kind":"text","language":"ru","text":"Содержимое документа","group_id":"source-document-id"}
{"dataset_id":"kalm-retrieval","record_id":"pair-id","kind":"retrieval","language":"en","text":"Search question","positive":"Correct document","negatives":["Different document"]}
```

Multimodal examples use `kind=image_text|ocr_text|video_text|meme_text|speech_text` plus `media_sha256`, calculated from **actual** media bytes. This JSONL stores hashes/labels only, not the media themselves; maintaining separate media files is a *future* task. The tool validates digest shape but **cannot prove the source media was downloaded or decoded**.
Do not label synthetic teacher-generated captions as human evidence. Preserve origin in a separately audited source manifest.

## Step 3: Normalize and partition a pilot

```bash
python3 research/pmr_datasets/prepare.py \
  --input data/pmr/source-export.jsonl \
  --audit data/pmr/audit.json \
  --approvals data/pmr/approvals.json \
  --out-dir data/pmr/pilot
```

Result: `train.jsonl`, `validation.jsonl`, `test.jsonl`, `manifest.json`. Exact normalized duplicate rows are discarded. Rows sharing a query, a positive passage, the same media digest, or an upstream group are forced into the same split.
**This is not an evaluation-ready corpus yet**: near-duplicate image/video checks, translated text leakage, weak labels, independently held-out Posta queries, benchmark overlap removal and source-family splits remain to be implemented.

## Offline tests

```bash
python3 -m unittest discover -s research/pmr_datasets -p 'test_*.py'
```

## Next engineering gates

1. Resolve missing licenses/revisions, especially BGE-M3 and MegaPairs's access acceptance; replace empty Common Voice mirror with reviewed official release.
2. Export *tiny* real text and image samples with upstream IDs from verified shards, without pulling full corpora.
3. Audit media byte availability, hashes, aspect ratios, captions/OCR language balance and independent source splits; produce `Posta-Dataset-v0.1` with source revision checksums and a frozen list of files.
4. Only then freeze the pilot PMR tokenizer/architecture and train the first randomly initialized checkpoint.

## Environment caveats

The current repo remains a Go service; the research scripts are **not invoked by Go or deployment**.
`data/` is ignored by `.gitignore`; raw training data and approval records must remain outside version control.
There is no validated dataset sample, model checkpoint, or quality benchmark result in this revision.
