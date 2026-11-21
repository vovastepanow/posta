#!/usr/bin/env bash
# Inspect up to 60 text/query records total; never media or training.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-source-preview-v1"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB" ] || exit 0
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] Source preview exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
cd "$APP"
umask 077
nice -n 10 timeout --signal=TERM --kill-after=30s 400 \
    "$PYTHON" research/pmr_datasets/sample_preview.py \
    --ids fineweb-edu-en fineweb2-ir-ru fineweb2-ir-en \
    --count 20 --out-dir "$ROOT/$JOB"
