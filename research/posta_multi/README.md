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
