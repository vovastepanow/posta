#!/usr/bin/env bash
# One-shot read-only Run 001 dataset/tokenizer/GPU preflight; no training.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run-001-preflight"
if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ] ||
   [ -e "$ROOT/$JOB.exit" ] || [ -e "$ROOT/$JOB.json" ]; then exit 0; fi
for path in "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" \
            "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
            "$APP/research/posta_multi/run001_text.py"; do
    [ -s "$path" ] || exit 0
done
for program in flock nohup timeout nice; do
    command -v "$program" >/dev/null || exit 0
done
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_preflight_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Read-only dataset/tokenizer preflight requested.\n'
