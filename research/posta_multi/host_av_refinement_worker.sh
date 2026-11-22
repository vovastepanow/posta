#!/usr/bin/env bash
# Controlled read-only review -> 8-step 235M AV refinement -> held-out check.
# Data and checkpoints remain on the private HDD. No production model changes.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB=posta-multi-av-geometry-refinement-v1
OUT="$ROOT/run001-av-geometry-refinement-v1"
SOURCE="$ROOT/run001-av-continued-v1/last.pt"
DATASET="$ROOT/Posta-Dataset-v0.1-screened"
TOKENIZER="$ROOT/run001-local-research-tokenizer-v1/tokenizer.json"
umask 077
mkdir -p "$ROOT/tmp" "$OUT"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2 PYTHONDONTWRITEBYTECODE=1
cd "$APP"
finish() {
    status=$?
    printf '%s\n' "$status" > "$ROOT/$JOB.exit"
    printf '[posta-av-v2] Worker finished with status %s\n' "$status"
}
trap finish EXIT
trap 'exit 143' INT TERM
[ ! -e "$ROOT/$JOB.exit" ] || exit 0
[ -s "$SOURCE" ] || { echo '[posta-av-v2] Missing 24-step checkpoint'; exit 1; }
[ -s "$ROOT/run001-av-continued-v1/heldout-after.json" ] || {
    echo '[posta-av-v2] Missing previous validation'; exit 1;
}
echo '[posta-av-v2] 1/4 Verify 8 independent heldout records of each modality'
if [ ! -s "$OUT/preflight.json" ]; then
    "$PYTHON" research/posta_multi/av_refine_preflight.py \
        --dataset "$DATASET" --limit 8 > "$OUT/preflight.tmp"
    mv "$OUT/preflight.tmp" "$OUT/preflight.json"
fi
echo '[posta-av-v2] 2/4 Evaluate frozen 24-step checkpoint on the larger pool'
if [ ! -s "$OUT/before.json" ]; then
    nice -n 10 timeout --signal=TERM --kill-after=90s 3600 \
        "$PYTHON" research/posta_multi/av_235m_eval.py \
            --dataset "$DATASET" --tokenizer "$TOKENIZER" \
            --checkpoint "$SOURCE" --limit 8 \
            --time-encoding relative-centered-v1 \
            --window-aggregation hierarchical-residual-v1 > "$OUT/before.tmp"
    mv "$OUT/before.tmp" "$OUT/before.json"
fi
echo '[posta-av-v2] 3/4 Train geometry-preserving positive alignment, 8 steps'
if [ ! -s "$OUT/weights/completed.json" ]; then
    nice -n 10 timeout --signal=TERM --kill-after=90s 21600 \
        "$PYTHON" research/posta_multi/av_235m_refine.py \
            --dataset "$DATASET" --tokenizer "$TOKENIZER" \
            --source "$SOURCE" --source-evaluation "$OUT/before.json" \
            --output "$OUT/weights" > >(tee -a "$OUT/train.log")
    "$PYTHON" - "$OUT/weights/last.pt" "$OUT/weights/completed.json" <<'PY'
import json, sys
from pathlib import Path
checkpoint, result = map(Path, sys.argv[1:])
if not checkpoint.is_file() or not checkpoint.stat().st_size:
    raise ValueError("refinement did not save a checkpoint")
result.write_text(json.dumps({
    "format": "posta-av-235m-geometry-refinement-completed-v1",
    "steps_completed": 8, "checkpoint_file_present": True,
    "production_promotion_allowed": False,
}), encoding="utf-8")
PY
fi
echo '[posta-av-v2] 4/4 Evaluate the new weights on the SAME eight examples'
if [ ! -s "$OUT/after.json" ]; then
    nice -n 10 timeout --signal=TERM --kill-after=90s 3600 \
        "$PYTHON" research/posta_multi/av_235m_eval.py \
            --dataset "$DATASET" --tokenizer "$TOKENIZER" \
            --checkpoint "$OUT/weights/last.pt" --limit 8 \
            --time-encoding relative-centered-v1 \
            --window-aggregation hierarchical-residual-v1 > "$OUT/after.tmp"
    mv "$OUT/after.tmp" "$OUT/after.json"
fi
"$PYTHON" research/posta_multi/av_refine_compare.py \
    --before "$OUT/before.json" \
    --after "$OUT/after.json" \
    --preflight "$OUT/preflight.json" > "$OUT/comparison.tmp"
mv "$OUT/comparison.tmp" "$OUT/comparison.json"
echo '[posta-av-v2] Measured comparison saved on private HDD. No automatic promotion.'
