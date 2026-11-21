#!/usr/bin/env bash
# Only provenance and CUDA availability. No model allocation or dataset copying.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run-001-preflight"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
finish() {
    code="$?"
    rm -f "$ROOT/$JOB.tmp"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] Preflight exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=10s 120 \
    "$PYTHON" research/posta_multi/run001_text.py \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --tokenizer "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
    --preflight > "$ROOT/$JOB.tmp"
mv "$ROOT/$JOB.tmp" "$ROOT/$JOB.json"
