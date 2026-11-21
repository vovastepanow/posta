#!/usr/bin/env bash
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-nanomiracl-ru-en-v1"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] NanoMIRACL benchmark exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=30s 1200 \
    "$PYTHON" research/posta_multi/nanomiracl_benchmark.py \
    --output "$ROOT/$JOB.json"
