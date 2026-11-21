#!/usr/bin/env bash
# Separate low-priority bounded GPU experiment; does not restart Posta or change old weights.
set -Eeuo pipefail

APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-retrieval-adam-v9"

finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-multi-v9] Exit %s; checkpoint %s; metrics %s\n' \
        "$code" "$ROOT/$JOB.pt" "$ROOT/$JOB-eval.json"
}
trap finish EXIT

# The one-shot sentinel prevents any cron repeats, including failed experiments.
if [ -e "$ROOT/$JOB.exit" ]; then
    printf '[posta-multi-v9] Already attempted.\n'
    exit 0
fi
mkdir -p "$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"

printf '[posta-multi-v9] Starting fixed, text-only AdamW retrieval experiment.\n'
# Time budget includes training plus retrospective validation. Existing sources are untouched.
nice -n 10 timeout --signal=TERM --kill-after=30s 7200 \
    "$PYTHON" research/posta_multi/next_retrieval_stage.py \
    --root "$ROOT" \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --tokenizer "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
    --comparison "$ROOT/posta-multi-compare-v8-retrieval-20261121-v1.json"
