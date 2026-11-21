#!/usr/bin/env bash
# Bounded to 200 RU and 200 EN judged queries; no large corpus or model.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-miracl-dev-400-v1"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] &&
    [ ! -e "$ROOT/$JOB.jsonl" ] || exit 0
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-miracl] Exit %s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp" PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
umask 077
nice -n 10 timeout --signal=TERM --kill-after=30s 1200 \
    "$PYTHON" research/posta_multi/miracl_benchmark.py \
    --queries-per-language 200 --data-file "$ROOT/$JOB.jsonl" \
    --output "$ROOT/$JOB.json"
