#!/usr/bin/env bash
# Optional one-shot, read-only embedding geometry analysis.
# This never starts a training process or overwrites model checkpoints.
set -Eeuo pipefail

APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-geometry-v1-v8-v9-20261121-v1"
HOLDOUT="posta-multi-holdout-v1-v8-v9-20261121-v1"

if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ] ||
   [ -e "$ROOT/$JOB.exit" ] || [ -e "$ROOT/$JOB.json" ]; then
    exit 0
fi
# Holdout must be finished (success or error) to avoid GPU competition.
if [ ! -s "$ROOT/$HOLDOUT.exit" ]; then
    exit 0
fi
for required in "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" \
                "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
                "$ROOT/posta-multi-retrieval-pilot-v1.pt" \
                "$ROOT/posta-multi-text-adam-v8.pt" \
                "$ROOT/posta-multi-retrieval-adam-v9.pt" \
                "$APP/research/posta_multi/compare_checkpoints.py"; do
    if [ ! -f "$required" ]; then
        printf '[posta-multi-geometry] Missing input: %s\n' "$required"
        exit 0
    fi
done
for cmd in flock nohup nice timeout; do
    command -v "$cmd" >/dev/null || exit 0
done
if ps -eo args | grep -Eq '[r]esearch/posta_multi/train_(text|retrieval|image|media)\.py'; then
    printf '[posta-multi-geometry] Another trainer is running; skip this tick.\n'
    exit 0
fi
# Close deploy lock FD9 for child so the update cron remains usable.
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_geometry_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-multi-geometry] Read-only validation geometry requested.\n'
