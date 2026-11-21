#!/usr/bin/env bash
# Bounded, READ-ONLY inventory of already-downloaded Posta datasets.
# Only aggregate JSON is written; no raw text, media reads, downloads or training.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-hdd-inventory-v1"
umask 077
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] HDD inventory exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$ROOT/python-libs:${PYTHONPATH:-}"
export OMP_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=10s 360 \
    "$PYTHON" research/pmr_datasets/local_training_inventory.py \
    --root "$ROOT" --out "$ROOT/$JOB.json"
