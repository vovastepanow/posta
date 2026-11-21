#!/usr/bin/env bash
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-nanomiracl-ru-en-v1"
[ ! -e "$ROOT/$JOB-frozen.json" ] || exit 0
if [ -e "$ROOT/$JOB.json" ]; then
    MODE="freeze"
    EXIT_PATH="$ROOT/$JOB-freeze.exit"
    OUTPUT="$ROOT/$JOB-backfill.json"
    EXTRA=(--compare-to-existing "$ROOT/$JOB.json")
else
    MODE="initial"
    EXIT_PATH="$ROOT/$JOB.exit"
    OUTPUT="$ROOT/$JOB.json"
    EXTRA=()
fi
[ ! -e "$EXIT_PATH" ] && [ ! -e "$OUTPUT" ] || exit 0
finish() {
    code="$?"
    printf '%s\n' "$code" > "$EXIT_PATH"
    printf '[posta-run001] NanoMIRACL %s exit=%s\n' "$MODE" "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=30s 1200 \
    "$PYTHON" research/posta_multi/nanomiracl_benchmark.py \
    --output "$OUTPUT" \
    --freeze-data "$ROOT/$JOB-frozen.json" "${EXTRA[@]}"
