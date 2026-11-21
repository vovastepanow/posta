# Posta Multi 235M — research architecture

This is a proposed, **untrained** multimodal retriever, implemented from standard
Transformer components. Core weights are initialized randomly; no pretrained
weights, production database, Go service or search path are used. The old PMR
name in dataset paths denotes the same research project. Size is approximately
235M; the configuration is not evidence of model quality.

## Architecture

| Block | Configuration | Function |
| --- | --- | --- |
| Text | 65,536-token vocabulary, 12 layers, width 768, 12 heads, FFN 3,072, up to 512 tokens | Shared encoder for queries and document/OCR/subtitle text; CLS pooling and 512-D projection. |
| Image | 224×224 RGB, 16×16 patches, 12 layers, width 768, 12 heads | Visual embedding; CLS pooling and 512-D projection. |
| Temporal | 3 layers, width 512, 8 heads, FFN 2,048 | Combines ordered video frame embeddings; optional relative timestamps. Also processes audio patches. |
| Audio input | 80-bin log-mel, 8×16 patches | Projects audio patches into the shared temporal encoder. |
| Post fusion | 1 layer, width 512, 8 heads; four masked slots | Combines available text, image, video and audio representations into one 512-D post vector. |

Queries and post content can be compared by cosine similarity. The post's OCR,
caption and subtitles must be serialized into its text input; truncation/chunking
and exact preprocessing are not implemented yet. Source/account quality is a
separate later ranking signal, not mixed into the post content encoder. Using
one image encoder per video frame is computationally expensive; frame sampling,
long-video segmentation and batched indexing remain to be engineered.

`spec.py` accounts for **235,179,777 parameters** in the default configuration.
The small logit-scale parameter is included. A tied output projection for masked
text pretraining reuses token embeddings and introduces no additional parameters.
The tokenizer itself has not yet been trained; 65,536 is a design budget, not
an existing tokenizer artifact. Exact language coverage and quality are unproven.

## Proposed training sequence

1. After dataset rights, privacy and held-out isolation are resolved, train a
   byte-level multilingual tokenizer from permitted **train-only** text and record
   its hash and special tokens. Never fit it on benchmark/evaluation data.
2. Train text on masked-token prediction and retrieval pairs. Handle examples
   without explicit negatives (e.g. in-batch negatives) and avoid false negatives
   from duplicate or semantically equivalent positives.
3. Align image/text and OCR, then video and audio; optionally use teacher outputs
   for distillation, without loading teacher weights into the core model.
4. Evaluate post and source separately using independent, frozen test sets;
   assess Russian/English and additional languages, media and new topics.

This is a proposed curriculum, not an implemented full trainer. A reproducible
trainer must save model and optimizer states, tokenizer/config and dataset
manifest hashes, code version, step, sampler state and Python/PyTorch CPU/GPU RNG
states; implement atomic checkpointing and verified resume. The currently
prepared dataset remains quarantined and cannot be treated as approved training
input. All tasks must be trained from data; architecture alone gives no semantic
ability or accuracy guarantee.

## Memory: lower bounds, not measurements

`python3 research/posta_multi/spec.py` prints exact analytical parameter counts.
The weights alone require about 0.88 GiB in FP32 or 0.44 GiB in FP16. Full FP32
weights + gradients + Adam first/second moments require at least 3.50 GiB before
activations, temporary buffers, framework and CUDA context. Thus ordinary FP32
Adam cannot fit wholly on a 3-GiB GPU. A pure FP16 weights/gradients + FP32
moments layout has a 2.63-GiB combined minimum before overhead, potentially
higher if FP32 master weights are kept. Offloading optimizers, checkpointing,
small microbatches and modality staging are possible approaches, **not implemented
or proven viable for a full training run here**.

Pascal/GTX 1060 needs a compatible PyTorch wheel; CUDA 13 libraries dropped
Pascal, and PyTorch's CUDA 12.6 wheel is a supported option for older GPUs:
https://docs.nvidia.com/cuda/cuda-features-archive/
https://pytorch.org/get-started/previous-versions/

## Offline and bounded tests

```bash
python3 research/posta_multi/spec.py
python3 research/posta_multi/cuda_probe.py
python3 -m unittest discover -s research/posta_multi -p 'test_*.py' -v
```

`cuda_probe.py` runs a synthetic 4-MiB driver/kernel test; it does not prove a
training configuration. `probe_torch.py` uses random data only and a strict GPU
allocator cap to check one small text/image/fusion SGD step and separate short
video/audio steps. It saves no model and does not access the corpus. Its result
cannot certify full-length sequences, Adam, training stability, or production
throughput. Do not run without checking available GPU memory on a shared desktop.

## Measured on the user's machine

On 21 November 2026, the reference model was instantiated with **235,179,777**
randomly initialized FP16 parameters on a GeForce GTX 1060 3GB using PyTorch
2.7.1 + CUDA 12.6. With the PyTorch allocator capped at 45% of device memory,
one synthetic SGD step (batch 1; 512 text tokens, one 224×224 image and fusion)
succeeded. Peak PyTorch allocation was 980,879,872 bytes, peak reserved memory
1,096,810,496 bytes; these figures do **not** include all CUDA context/library
memory. Separate synthetic backward/SGD steps also succeeded for a two-frame
video and a short 80×64 log-mel input. The [machine-readable local result]
`/mnt/hdd_ubuntu/posta-pmr/posta-multi-512-gpu-probe.json` records timings and
caveats. Unit tests also checked the parameter count on a meta device, output
shapes, masks, a small training step and a checkpoint round trip.

This establishes only that the stated **short synthetic FP16 SGD** operations
run on this machine. It does not establish that full multimodal training,
Adam/AdamW, bigger batches, many video frames, long audio, numerical stability,
useful convergence or any target model quality are feasible. No pretrained
weights or actual dataset examples were used, and no trained checkpoint was
created. Actual timing per useful example and end-to-end training time remain
unmeasured. GPU memory was shared with desktop applications and varies.

Run the bounded test with the dedicated environment installed on the HDD:

```bash
PYTHONPATH=/mnt/hdd_ubuntu/posta-pmr/torch-cu126:/mnt/hdd_ubuntu/posta-pmr/python-libs \
TMPDIR=/mnt/hdd_ubuntu/posta-pmr/tmp OMP_NUM_THREADS=2 \
/home/v0v4/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \
research/posta_multi/probe_torch.py --sequence-length 512
```

## Requirements and next implementation steps

The architecture is a prototype, not evidence that Posta's quality or speed
requirements are met:

- Multilingual natural-language search: text encoder and a retrieval loss exist;
  tokenizer, training data pipeline and measured Russian/English quality do not.
- Images and memes: image and text encoders exist; OCR extraction, alignment
  training and tests of combined visual/text understanding are absent.
- Video and audio: encoders accept short prepared inputs; sampling, decoding,
  long-media handling and demonstrated understanding of actions are absent.
- Post and source: content embedding exists; independent source ranking is absent.
- New topics, top-100 latency and deduplication: no representative evaluation,
  indexing implementation or quality/latency measurements exist.
- Training from scratch: random initialization exists; full reproducible trainer,
  approved corpus, convergence and practical training duration remain unverified.

`loss.py` provides a query-to-candidate contrastive objective with multiple
positives and ignored pairs. Every query needs an explicit positive; all
unmasked non-positive pairs act as negatives, so the data pipeline must identify
or mask known duplicates and ambiguous pairs. This is an objective component,
not a trainer or a guarantee against false negatives. The next steps are to
implement and evaluate a tokenizer and a reproducible, resumable training loop,
then measure retrieval quality on held-out data before expanding modalities.

## Synthetic training and resume smoke test

`synthetic_train.py` is a CPU-only miniature model and synthetic random-token
exercise of retrieval loss, AdamW, atomic checkpoint saving and resuming. It
uses the same token sequence for both sides of each synthetic pair; it is not a
semantic task, does not train the 235M configuration and produces no useful
model. It does not read datasets. The checkpoint includes the model, optimizer,
configuration, step and PyTorch CPU RNG state. The included test confirms that
an interrupted and resumed four-step run matches an uninterrupted run on this
CPU environment. This is not yet a real-data trainer: dataset/tokenizer hashes,
sampler state, CUDA RNG, mixed precision and GPU memory management are missing.

Example (use a new checkpoint path; `--steps` is the total, not extra steps):

```bash
PYTHONPATH=/mnt/hdd_ubuntu/posta-pmr/torch-cu126:/mnt/hdd_ubuntu/posta-pmr/python-libs \
/home/v0v4/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \
research/posta_multi/synthetic_train.py --steps 2 --checkpoint /tmp/posta-synthetic.pt
# Add --resume and set --steps 4 to continue to step 4.
```

## Provisional tokenizer and data interface

`tokenizer.py` supplies a deterministic UTF-8 byte tokenizer with CLS, SEP,
PAD and MASK (260 IDs). It is a temporary integration baseline, not the planned
65,536-token learned multilingual tokenizer: Russian and other non-ASCII text
use multiple tokens per character, so 512 tokens covers less text. It does not
need to be trained and has not been fitted on the quarantined corpus. Changing
tokenizers later requires reinitializing or explicitly remapping embeddings.

`dataset.py` provides an adapter for retrieval train rows, checking explicit
manifest status, row approval, provenance, split and file SHA-256, then yielding
tokenized query/positive pairs. Its approved manifest status is an interface
contract, not a claim that permissions were independently verified; setting it
by hand does not clear rights, privacy or benchmark overlap. Existing screened
corpus remains quarantined and is rejected. The adapter is not connected to a
full training loop and does not yet batch examples or choose safe negatives.

## NVIDIA OCR input smoke test

`ocr_smoke.py` independently reads the hash-verified local `ocr_text-train`
file and restricts rows to NVIDIA's pinned OCR-Synthetic-Multilingual-v1
revision. NVIDIA's dataset card states CC BY 4.0 and use for commercial and
noncommercial purposes: https://huggingface.co/datasets/nvidia/OCR-Synthetic-Multilingual-v1
Attribution must be preserved. A bounded test on four actual OCR text labels
ran one CPU masked-byte update on a tiny model with finite loss and gradients.
It does not use the images, train 235M parameters, save a checkpoint, establish
model quality, or imply clearance of other dataset sources. The general
retrieval-data approval gate is unchanged. The local screened corpus remains
marked quarantined in its manifest; this test does not alter that manifest.

## Multi-source integration smoke test

`corpus.py` streams the seven task types from the local manifest and checks the
selected file SHA-256, row kind/split and provenance; media access also checks
path confinement and SHA-256. It reports the existing manifest status without
claiming that metadata flags establish or remove upstream rights. The corpus
still contains unresolved source-specific, privacy and evaluation caveats in
`research/pmr_datasets/RESULT_2026-11-21.md`.

`corpus_smoke.py` runs inference on one real record of each type. The separate
`real_train_smoke.py` performs one CPU update on a **tiny random model** for
each task, using one raw-text masked-byte example and two examples for each
retrieval/alignment task. It reads real images, a single video frame and 0.25 s
of audio. Hashes, source revisions and splits are checked. These tiny tests do
not establish retrieval quality or clear false negatives; in-batch negatives
are only checked for exact matching text/media/group. They save no checkpoint.
The experimental audio features and image preprocessing are not validated as a
final model input specification. The temporary byte tokenizer is not final.

This is not a full training system. Required work includes a learned tokenizer,
full-size memory-feasible optimizer and numerical stability, reproducible
resumable data sampling/checkpoints, longer media, safe negatives, meaningful
held-out evaluation and measured runtime. No full 235M training run has been
performed. The GTX 1060 was visible only outside the restricted sandbox; its
3 GiB VRAM does not fit ordinary FP32 Adam for this model.

## Experimental CPU-master optimizer

`cpu_master_sgd.py` keeps FP16 model parameters on the accelerator and FP32
master parameters in CPU RAM, uses fixed loss scaling and checks all gradients
for overflow before updating. It implements SGD without momentum, not Adam;
it has an optimizer state serialization API. Tests cover numerical updates,
restored continuation and skipping the entire update on overflow. It is
experimental: there is no automatic loss-scale adjustment, gradient clipping,
production checkpoint manager or demonstrated training convergence. CPU RAM
and PCIe transfer costs are significant.

`master_probe.py` ran one synthetic masked-token step on the complete
235,179,777-parameter model on the GTX 1060 3GB with a 32-token sequence and
FP32 CPU master weights. The step succeeded, the text embedding changed, and
peak PyTorch GPU allocation was 1,032,117,760 bytes (reserved 1,056,964,608
bytes); CUDA context and desktop usage are excluded. Initialization plus step
took 4.84 seconds in this one run, not a sustained throughput measurement. No
checkpoint was saved and no dataset was used. This does not establish that
long sequences, image/video/audio, full training, numerical stability or
useful learning will fit or converge.

## Resumable OCR text training experiment

`train_text.py` provides a bounded masked-byte text training experiment using
NVIDIA OCR training labels from the screened corpus. It verifies the corpus
file hash, pins source IDs and revision, records the corpus manifest and
selected token-sequence hashes and tokenizer version, and saves model,
optimizer and CPU/CUDA RNG state with atomic replacement. Resume rejects changed
settings or data. Steps are sampled from the first N eligible records; this is
not a representative data sampler. Tiny mode uses CPU AdamW; full mode uses
experimental FP16 GPU parameters and CPU-master SGD. This is a text-only
experiment, not a multimodal trainer or an optimized training recipe.

On the user's GTX 1060 3GB, a full 235M model completed one step on real OCR
text, saved a 1.4 GiB checkpoint on the HDD, restored it in a separate process
and completed a second step. The checkpoint is
`/mnt/hdd_ubuntu/posta-pmr/posta-multi-text-probe-001.pt`; it is a short-lived
experimental checkpoint with a provisional byte tokenizer, not a useful or
production-ready model. Loss values were finite; two steps do not demonstrate
learning quality or training stability. The exact resume equivalence test is on
the tiny CPU model, not the full GPU model. Checkpoint replacement temporarily
needs space for both versions; full mode requires at least 4 GiB free.

Remaining for full training: train and validate the final tokenizer; implement a
representative multilingual sampler, longer runs, validation metrics, safe
contrastive negatives, and actual image/video/audio training and checkpoint
resume for those stages. Source-specific rights and benchmark limitations still
apply. Avoid launching long runs from this prototype until those are addressed.

## Experimental BPE tokenizer

`train_tokenizer.py` builds a byte-level BPE vocabulary from balanced NVIDIA OCR
training labels only. The experimental tokenizer is stored on the HDD at
`/mnt/hdd_ubuntu/posta-pmr/tokenizer-ocr-experiment-v1/`; its metadata records
the source revision, manifest, text and tokenizer hashes. It was trained on
500 Russian and 500 English samples, resulting in 52,318 tokens (65,536 is the
maximum model vocabulary). This is an OCR-only experiment, not a representative
final multilingual vocabulary. The required `tokenizers==0.22.1` dependency is
installed in `/mnt/hdd_ubuntu/posta-pmr/tokenizer-libs`.

`train_text.py --tokenizer /path/to/tokenizer.json` can use this tokenizer,
checks its metadata hash and special IDs, and records the tokenizer hash in the
checkpoint; incompatible checkpoints cannot be resumed. A tiny CPU run on
real OCR texts saved and resumed successfully with this tokenizer. The earlier
full-size checkpoint used the provisional byte tokenizer and cannot resume with
BPE; its embeddings cannot simply be reused under different token IDs.

PyTorch, supporting Python libraries, training data and the large experimental
checkpoint already reside on the HDD. The Codex runtime under `~/.cache` is
shared with the application and has not been moved. `/tmp` is a RAM filesystem
and was nearly full during testing; store larger temporary files on the HDD via
`TMPDIR=/mnt/hdd_ubuntu/posta-pmr/tmp`.

## Mixed-text pilot

`train_bpe.py` trains an experimental 65,536-token byte BPE vocabulary from
balanced Russian and English training records across text, OCR, image captions,
retrieval queries and video captions; validation checks are held out by local
split, not independent benchmarks. Tokenizer files and statistics are under
`/mnt/hdd_ubuntu/posta-pmr/tokenizer-mixed-experiment-v1`.

`train_text.py --data-mode mixed` selects equal record counts in eight
language/task strata and splits long texts into token windows. The training
sampler draws windows uniformly, so record and language weights can differ from
the initial quotas. The masked-token objective masks 15% of non-special tokens
(up to 64 per example). `--warm-start` can initialize from a checkpoint with
the same architecture and tokenizer while recording its SHA-256 and step;
`--resume` enforces unchanged dataset, objective and tokenizer settings. The
pilot remains text-only, and its corpus is small relative to full pretraining.

`eval_text.py` measures masked-token loss and accuracy on a bounded set of local
validation texts and OCR labels, not training records. The checkpoint must
match the dataset manifest and tokenizer hash. After 32 pilot steps, two
examples per task/language gave losses of 10.99–11.14; this tiny sample does not
measure retrieval or multimodal ability, and the model is still untrained for
practical use. After the pilot, longer training resumed with checkpoints every
32 steps and a log at `/mnt/hdd_ubuntu/posta-pmr/posta-multi-mixed-pilot-v2.log`.
The vocabulary and sample corpus remain experimental; validation splits are not
independent external benchmarks.

## Retrieval and media pilot steps

The 235M pilot reached 128 mixed text steps, 32 explicit-negative retrieval
steps and 16 image-text steps on the GTX 1060, saving separate checkpoints on
the HDD. On 16 local held-out retrieval pairs, the text checkpoint ranked the
positive above one explicit negative 11 times and the retrieval checkpoint 13
times. On eight held-out caption/image pairs, the retrieval checkpoint got 3
and the image-trained checkpoint 4. These samples are far too small to infer
useful quality and do not measure top-k retrieval or independent benchmarks.

`train_retrieval.py` uses explicitly provided negatives; positive-only records
are excluded in this pilot. `train_image.py` aligns two caption/image pairs per
step; duplicate text and image hashes are filtered, but semantic false
negatives remain possible. Both stages pin data and tokenizer hashes, save RNG
and optimizer state, and support resumption. `train_media.py` performed two
experimental video alignment steps (up to two decoded frames per clip). A
separate two-step audio smoke test cropped speech to 0.25 seconds while pairing
it with a complete transcript; this is not temporally aligned training data
and its checkpoint must not be used as evidence of audio learning. The script now processes full utterances by pooling segments. More extensive
audio and video training and quality evaluation remain outstanding.

The checkpoints are experiments, not production models. See the saved-step
logs with `python3 research/posta_multi/status.py`; the logs report saved state,
not whether a process is currently running.

### Aligned audio revision

The earlier 0.25-second audio pilot was misleading and is superseded. Audio
training now selects whole utterances (up to 12 seconds), divides their
log-mel frames into segments, encodes every segment and pools them to compare
with the complete transcript. Gradient checkpointing reduces GPU activation
memory. An initial short-utterance run completed eight steps. Its four-example
held-out test ranked the correct audio over one alternative 3/4 both before and
after training; this is too small to establish improvement. The segment-pooled
revision was then started on 32 whole utterances. These experiments remain
small and do not demonstrate speech recognition, alignment quality or useful
multimodal retrieval. Source and split restrictions still apply.

On 16 fixed held-out audio pairs, the image-stage baseline ranked the matching
utterance above one alternative 10 times; the 32-step pooled-audio checkpoint
ranked it 9 times, and the subsequent 64-step checkpoint 10 times. These tiny
samples show no demonstrated improvement. On the same 16 retrieval pairs the
latter checkpoint scored 12/16 (the earlier retrieval checkpoint scored 13/16);
on 8 image pairs it scored 4/8. These local pairwise comparisons are not
production quality measurements. Full video and audio understanding remain
unproven.

A later audio experiment uses four complete utterances per step and in-batch
contrastive negatives. This remains a small experiment on 128 selected short
utterances; unrelated-looking transcripts can still be false negatives. Its
checkpoint and training log are `posta-multi-audio-batch4-v5.pt` and `.log` on
the HDD. Larger-batch training can affect the already trained text encoder, so
retrieval and image quality should be rechecked after the run.

An additional audio-only experiment freezes the text and image encoders and
trains the shared temporal/audio block with FP32 CPU-master AdamW. Its optimizer
is tested against PyTorch AdamW on a small example and preserves moments on
resume. Because the temporal block is shared with video, video behaviour can
still change. At 128 steps, the held-out 16-utterance text-to-audio Recall@1
was 1/16, equal to the image-stage baseline; this is not evidence of useful
audio understanding. All 16 validation utterances are from one speaker. The
run uses 128 training utterances and is intended as an experiment, not a
sufficient corpus or final model.

After 512 AdamW audio steps, the 16-pair validation Recall@1 was 1/16, the
same as before audio training. On 16 selected training utterances it was 5/16;
this suggests memorization rather than demonstrated generalization. A follow-up
experiment expands from 128 to 384 complete utterances (up to 16 seconds),
retains text/image encoder freezing and pools every audio segment. The saved
checkpoint is `posta-multi-audio-expanded-v7.pt` on HDD. Validation remains
limited to one held-out speaker and is not a broad speech benchmark.

## Current handoff

Audio now uses whole utterances split into segments, with matching complete
transcripts. Audio-only AdamW (text/image frozen; temporal/video shared) ran
512 steps on 128 utterances and another 256 steps on 384 utterances. On 16
local validation audio pairs the expanded model's Recall@1 was 0/16; the
pre-audio baseline was 1/16. These are too small for firm conclusions, but do
not show audio generalization. On 8 video pairs the respective results were
4/8 and 6/8; this tiny check suggests possible shared-layer regression.
Validation audio is from one speaker. Retain previous checkpoints.

Text-only AdamW was initialized from the image-stage checkpoint, using mixed
text and frozen non-text weights. Two steps completed and the resumable run to
128 steps was started; progress is in `posta-multi-text-adam-v8.log` on HDD.
Check the log and process before asserting it is still running. Resume with
`--data-mode mixed --model-size full --samples 128 --text-only-adam
--learning-rate 0.0001` and the same tokenizer and checkpoint. The image and
retrieval checkpoints remain available. Next evaluate held-out text/retrieval
and continue training; full multimodal quality is not achieved.

## Comparing existing checkpoints (read-only, 21 Nov 2026)

`compare_checkpoints.py` sequentially evaluates the already saved HDD checkpoints
against identical *local validation* records. It does not train or rewrite weights,
download datasets, change production data, or publish media. Its default selection
is mixed-text, retrieval, image, expanded-audio and text-Adam checkpoints; default
tasks are masked-token text and retrieval. To include all modalities:

```bash
cd /var/www/html/posta
PYTHONPATH=/mnt/hdd_ubuntu/posta-pmr/torch-cu126:/mnt/hdd_ubuntu/posta-pmr/python-libs:/mnt/hdd_ubuntu/posta-pmr/tokenizer-libs \
TMPDIR=/mnt/hdd_ubuntu/posta-pmr/tmp OMP_NUM_THREADS=2 \
/home/v0v4/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \
research/posta_multi/compare_checkpoints.py --tasks text retrieval image video audio
```

Default report: `/mnt/hdd_ubuntu/posta-pmr/posta-multi-comparison-v1.json`.
For an initial short comparison, select `--checkpoints retrieval-v1 text-v8 --tasks retrieval`.
The runner records per-task errors without destroying partial reports and refuses
to declare an automatic winning model. Incomplete relevance labels, different
checkpoint objectives and the small, potentially biased validation split prevent
strong quality claims. The model still needs a genuinely independent benchmark
and verified source-rights/privacy gates before serious additional training.

## One-shot local comparison through the existing deploy cron

The existing CI-gated host updater invokes `host_compare_hook.sh` **after** a
successful normal deployment. It returns immediately and starts one bounded,
read-only comparison in a separate lower-priority background process. This is
not a general remote command executor, runner, or training loop.

Only `retrieval-v1` and `text-v8` are compared on local validation **text and
retrieval** tasks. The hook verifies that the HDD, Python runtime, tokenizer and
both checkpoints exist before starting; the worker uses `flock`, a 2-hour
overall timeout and a fixed request ID to prevent repeated jobs on successive
deploys. Existing `.pt` files are only read. Nothing is uploaded to GitHub;
no training starts and production DB is not accessed.

Outputs:
- `/mnt/hdd_ubuntu/posta-pmr/posta-multi-compare-v8-retrieval-20261121-v1.log`
- `/mnt/hdd_ubuntu/posta-pmr/posta-multi-compare-v8-retrieval-20261121-v1.json`
- `/mnt/hdd_ubuntu/posta-pmr/posta-multi-compare-v8-retrieval-20261121-v1.exit`

A nonzero `.exit` records that the comparison failed; it does not damage
checkpoint files or stop the Posta service. A new request requires a reviewed
new request ID, rather than automatically rerunning a possibly broken job.
This mechanism cannot send results back to ChatGPT autonomously: the JSON must
be supplied by the owner or an appropriately scoped results connector.

## Guarded v9 retrieval experiment

The next one-shot research experiment is opt-in through the already active local
cron and gated by the completed read-only comparison above. The small
`next_retrieval_stage.py` planner reads **only** its status-0 report and checks
that the existing `retrieval-v1` and `text-v8` checkpoint evaluations used the
same 16 validation pairs. It chooses `text-v8` only when its pair accuracy
and MRR are no worse; otherwise it takes `retrieval-v1`. This is a provisional
experiment-source rule, **not** evidence of production quality.

The v9 stage uses CPU-master FP32 AdamW with the 235M FP16 network on the GTX
1060, trains only text tokens/encoder/projection, and samples 512 distinct
training pairs reproducibly from the *entire* retrieval train split using
stable SHA-256 selection. Training is capped at **64 updates**, checkpointing
every 16, process niceness +10, and a 2-hour overall timeout. Existing v1/v8
checkpoints are never written. The 45%-GPU-allocation limit from the trainer
still applies; this is a limited research experiment, not production training.

Cron calls `host_retrieval_hook.sh` every tick only on a clean checkout.
Without a successful first comparison, it does nothing. It refuses to launch
over another trainer, a prior attempt, or a partially existing v9 checkpoint.
The worker closes the deployer's inherited lock file descriptor so training
does not block future Posta updates. It does not read Git credentials, expose
media, or alter the Go service.

Result files remain strictly on the HDD:
- `posta-multi-retrieval-adam-v9.log`: progress and errors.
- `posta-multi-retrieval-adam-v9.pt`: *new* checkpoint if produced.
- `posta-multi-retrieval-adam-v9-eval.json`: text/search comparison, if completed.
- `posta-multi-retrieval-adam-v9.exit`: one-shot exit code.

A nonzero exit preserves prior checkpoints. The stage must not be automatically
promoted or repeated based solely on a 16-query local validation score. Data
rights/privacy and independent evaluation remain open prerequisites for larger
runs and deployment.



## Consistent local cross-modal ranking metrics (21 Nov 2026)

`eval_image.py`, `eval_video.py`, and `eval_audio.py` now report
conservative Recall@1/5/10 and MRR across their selected validation media
pools, plus the earlier one-alternative pair accuracy. Equal similarity
scores are treated as ties that **do not** rank the positive first; the old
audio code counted every tie as a top-1 success. These are **not** unbiased
production recall estimates: the candidate pools are small and some
unlabeled alternatives can be semantically relevant.

The v9 launch gate additionally checks the on-HDD comparison report's exact
corpus path, tokenizer path, source checkpoint names, and evaluated tasks.
This prevents a report for unrelated local data from starting training. The
`status.py` output now includes each unattended job's on-disk exit marker
and report existence (the markers do not themselves prove a live process).

## Local v9 completion and result visibility

As of the user's 2026-11-21 local status output, the bounded `v9` AdamW
experiment completed **64 steps** and saved a checkpoint, with training
`last_loss=0.69638`. The earlier comparison and the v9 process both reported
exit code 0. This does **not** establish that retrieval metrics improved:
the two-candidate training objective has an uninformed loss of approximately
`ln(2)=0.693`, and only the held-out search rankings can assess improvement.

`status.py` now reads the correct v9 result file
`posta-multi-retrieval-adam-v9-eval.json` (rather than the nonexistent
`posta-multi-retrieval-adam-v9.json`), displays small numeric summaries from
both local reports, and shows which earlier checkpoint initialized v9. It does
not load the model weights, run evaluation, or publish any private input text.
Metrics remain derived from a very small local validation set and must not
be interpreted as independent search-quality evidence.



## Test split regression audit after v9 search degradation

The local 16-query validation comparison found a substantial `v9` regression
relative to `retrieval-v1`: pair accuracy 7/16 vs 13/16, Recall@1 1/16 vs
3/16, Recall@5 2/16 vs 8/16, and MRR 0.1352 vs 0.3469. Masked-token loss
also rose for all four text/language groups. These are real observations on
the **same local validation selection** but are too small for generalization
claims. The initial `v9` checkpoint source could be `text-v8` rather than
`retrieval-v1`; check the log's JSON `source` before attributing the entire
difference to AdamW training. Do not promote `v9`.

The next **read-only** experiment evaluates all three (`retrieval-v1`,
`text-v8`, `retrieval-adam-v9`) on the corpus's separate `test` split. It
chooses 64 eligible query/positive/explicit-negative records from the **whole
test split** by stable SHA-256 ordering (`eval_retrieval.py --split test
--sample-hash --samples 64`). Candidate passages are deduplicated within the
evaluation pool. The comparison verifies identical selected-pair hashes across
checkpoints. The original 16-query validation remains untouched. The test
split is internal and has incomplete relevance labels, **not** an independent
production benchmark; once inspected, it should not be reused for training
selection or repeated hyperparameter tuning.

The existing deployment cron starts this analysis **once** after the v9
completion marker reports exit 0 and all three existing checkpoints are
present. It is low-priority, bounded by a 2-hour timeout, acquires a separate
lock and only writes to HDD:
- `posta-multi-holdout-v1-v8-v9-20261121-v1.json`
- `posta-multi-holdout-v1-v8-v9-20261121-v1.log`
- `posta-multi-holdout-v1-v8-v9-20261121-v1.exit`

No `.pt` file is modified; no data are uploaded to GitHub. `status.py`
reports the small numeric comparison after it has finished. Avoid new
long-running training until the resulting cross-check is analyzed and dataset
rights/privacy review is complete.

## Diagnosing retrieval-v9 embedding geometry without retraining

A separate one-shot, read-only validation diagnostic starts after the prior
64-query test-split comparison finishes, preventing GPU competition. It uses
the original **16 validation query pairs**, never the test split, and evaluates
the existing v1, v8 and v9 weights sequentially. The opt-in
`eval_retrieval.py --diagnostics` flag reports:
- mean positive and explicit-negative cosine, their average/median margins;
- fraction of positive margins, range and spread of margins;
- inter-example cosine between distinct query embeddings and distinct document
  embeddings (near 1 may indicate collapse).

This does not provide causal proof: a change in source initialization (v8 vs
v1), optimization, negative sampling and small pool composition may all
contribute to v9 regression. No automatic model selection/promotion or new
training follows this diagnostic.

Output files on the HDD:
`posta-multi-geometry-v1-v8-v9-20261121-v1.json`,
`.log` and `.exit`. The report contains aggregated numbers and checkpoint
labels, never source texts or media. Existing checkpoints remain unchanged.

## Retrieval split coverage and leakage audit (read-only, after holdout)

The screened candidate has 22,933 retrieval records and 5,315 records without
explicit negative labels. Earlier training and evaluation load only rows with
explicit negatives, which may cause a selection bias. Do not interpret its
current benchmark scores as general topic/language quality.

`retrieval_split_audit.py` verifies each split through `Corpus.rows`, then
reports aggregate per-language and per-source record counts, fraction with
explicit negatives, duplicate/conflicting negatives and normalized exact
overlap between train, validation and test:
- same query and positive pair across splits;
- same query text and same positive document text across splits;
- positives that occur as labeled negatives in another split.

The auditor hashes NFKC/casefold/whitespace-normalized text for comparisons.
The report contains **only counts**, not user text, document hashes, IDs or media.
Exact matching cannot rule out near-duplicates, translations or unknown
benchmark contamination; some reuse may be intentional and requires review.

The established cron runs it once, **after** the existing embedding geometry
job has finished, to avoid simultaneous HDD-heavy jobs. This task is CPU-only
and never writes checkpoints or changes the corpus. Files stay on the HDD:
`posta-multi-retrieval-split-audit-20261121-v1.json`, `.log`, `.exit`.

## No-GPU historical v9 comparison and fail-closed decision

Two pre-existing reports evaluated v1/v8 and v1/v9 on the **same original
16 local validation queries**. `checkpoint_decision.py` checks equal
manifest/tokenizer provenance and the per-record selection hash, uses v1 as an
anchor to verify that reports agree, then compares v9 with the **actual
training initialization** recovered from the v9 log. It calculates deltas in
pair accuracy, Recall@1/5/10 and MRR. Nonfinite values, missing/duplicate
rows, mismatched selections, unknown source or inconsistent v1 anchor fail
closed rather than producing a verdict. This is a post-experiment analysis,
not a trained model selector.

A small synchronous hook in the existing cron invokes the stdlib-only script
once after the reports exist. Its output stays on the HDD:
`posta-multi-v9-decision-20261121-v1.json`, `.log` and `.exit`.
`status.py` displays the resulting verdict and deltas numerically.

**No candidate is ever promoted automatically.** Even if the 16-query
validation looks better, independent evaluation, rights/privacy review and
production integration are still incomplete. The audit is only read-only
metadata aggregation and does not launch or authorize another training stage.

## Optional future-only retrieval pair curation

Earlier v1/v9 training used the first listed explicit negative for each
query/positive record, without checking whether that negative was a known
positive for the *same query* in a different row. This can introduce
contradictory supervision. **It is a potential risk, not a proven cause of
the v9 regression.**

`retrieval_data_quality.py` provides a separately versioned, opt-in sampler:
- normalizes NFKC, Unicode case and whitespace for exact comparisons;
- reads all known positives per normalized query, then excludes negatives
  contradicting any such positive;
- rejects negatives identical to the query or empty after normalization;
- excludes train queries/positives found in validation/test (exact normalized
  match), keeps one reproducibly chosen triple per distinct query;
- deterministic SHA-256 selection independent of input-row and negative-list
  ordering; numeric counts only and an immutable pair-selection digest.

Future experimental retrieval trainers can select `--curated-pairs
--sample-hash`, which records a different sampler policy in checkpoint
metadata. **No earlier checkpoint or frozen evaluation selection is changed.**
The feature is not enabled in any cron worker; it does not mean the dataset
is approved for training. It cannot rule out paraphrases, translations,
unlabeled relevant negatives, rights/privacy problems or near-duplicates.
