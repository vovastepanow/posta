#!/usr/bin/env bash
# Runs only the two selected existing checkpoints and two local validation tasks.
# Do not use for training or handling GitHub secrets.
set -Eeuo pipefail

APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
RUN_ID="posta-multi-compare-v8-retrieval-20261121-v1"
STATUS="$ROOT/$RUN_ID.exit"
REPORT="$ROOT/$RUN_ID.json"

# A prior result (even a failed one) should never launch a duplicate workload.
if [ -e "$STATUS" ]; then
    printf '[posta-multi-compare] Already attempted; refusing duplicate.\n'
    exit 0
fi
finish() {
    code="$?"
    printf '%s\n' "$code" > "$STATUS"
    printf '[posta-multi-compare] Exit code %s, report %s\n' "$code" "$REPORT"
}
trap finish EXIT

mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"

printf '[posta-multi-compare] Start read-only comparison of retrieval-v1 and text-v8\n'
# timeout stops the evaluation, never touches the existing .pt files.
timeout --signal=TERM --kill-after=30s 7200 \
    "$PYTHON" research/posta_multi/compare_checkpoints.py \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --tokenizer "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
    --checkpoint-dir "$ROOT" \
    --checkpoints retrieval-v1 text-v8 \
    --tasks text retrieval \
    --timeout 1200 --output "$REPORT"
