#!/usr/bin/env bash
# One bounded source ingest, not a training or production indexing action.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-content-samples-v1"
OUTPUT="$ROOT/$JOB"
REPORT="$ROOT/$JOB-summary.json"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$OUTPUT" ] || exit 0
umask 077
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] Content source quarantine exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$APP/research/pmr_datasets"
cd "$APP"
# Bounded 200 rows per source; raw texts only on the private HDD.
nice -n 10 timeout --signal=TERM --kill-after=20s 1200 \
    "$PYTHON" research/pmr_datasets/harvest_content_sources.py \
    --out-dir "$OUTPUT" \
    --count-per-source 200 --batch-size 5 --max-chars 16000 \
    > "$REPORT.tmp"
mv "$REPORT.tmp" "$REPORT"
