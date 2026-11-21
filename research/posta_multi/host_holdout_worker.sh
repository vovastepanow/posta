#!/usr/bin/env bash
# Comparisons only: no optimizer step and no checkpoint output.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-holdout-v1-v8-v9-20261121-v1"

if [ -e "$ROOT/$JOB.exit" ]; then exit 0; fi
finish() {
    code="$?"
    printf '%s\n' "$code" >"$ROOT/$JOB.exit"
    printf '[posta-multi-holdout] Finished with code %s\n' "$code"
}
trap finish EXIT

mkdir -p "$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=30s 7200 \
    "$PYTHON" research/posta_multi/compare_checkpoints.py \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --tokenizer "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
    --checkpoint-dir "$ROOT" \
    --checkpoints retrieval-v1 text-v8 retrieval-adam-v9 \
    --tasks retrieval-test \
    --timeout 1800 \
    --output "$ROOT/$JOB.json"
