#!/usr/bin/env bash
# Read-only one-time baseline; do not read model weights or upload corpus.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run001-bm25-validation-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
[ -s "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" ] || exit 0
[ -s "$APP/research/posta_multi/bm25_baseline.py" ] || exit 0
for program in flock nohup nice timeout; do command -v "$program" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_bm25_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] One-time read-only BM25 validation benchmark requested.\n'
