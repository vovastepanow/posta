#!/usr/bin/env bash
# Bound CPU and I/O usage; write aggregate numbers to local HDD only.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run001-bm25-validation-v1"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
finish() {
    code="$?"
    printf '%s\n' "$code" >"$ROOT/$JOB.exit"
    printf '[posta-run001] BM25 validation exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=30s 900 \
    "$PYTHON" research/posta_multi/bm25_baseline.py \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --samples 64 \
    --output "$ROOT/$JOB.json"
