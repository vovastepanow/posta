#!/usr/bin/env bash
# No training, no private text output, no checkpoint writes.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-retrieval-split-audit-20261121-v1"
if [ -e "$ROOT/$JOB.exit" ] || [ -e "$ROOT/$JOB.json" ]; then exit 0; fi
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-multi-split-audit] Exit %s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=30s 3600 \
    "$PYTHON" research/posta_multi/retrieval_split_audit.py \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --output "$ROOT/$JOB.json"
