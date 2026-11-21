#!/usr/bin/env bash
# One-shot read-only model evaluation requested through the green main deploy.
# Never trains or modifies a checkpoint. Immediately returns to the deployer.
set -Eeuo pipefail

APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
RUN_ID="posta-multi-compare-v8-retrieval-20261121-v1"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"

note() { printf '[posta-multi-compare] %s\n' "$*"; }
if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ]; then
    note "HDD or model runtime unavailable; skipped."
    exit 0
fi
for command in flock nohup timeout; do
    command -v "$command" >/dev/null || { note "$command unavailable; skipped."; exit 0; }
done
for required in "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" \
                "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
                "$ROOT/posta-multi-retrieval-pilot-v1.pt" \
                "$ROOT/posta-multi-text-adam-v8.pt" \
                "$APP/research/posta_multi/compare_checkpoints.py"; do
    if [ ! -f "$required" ]; then
        note "Required input missing: $required; skipped."
        exit 0
    fi
done

if [ -e "$ROOT/$RUN_ID.exit" ]; then
    note "Comparison already attempted; not repeating."
    exit 0
fi
# flock locks the entire child operation, not just the startup shell.
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$RUN_ID.lock" \
    bash "$APP/research/posta_multi/host_compare_worker.sh" \
    9>&- </dev/null >>"$ROOT/$RUN_ID.log" 2>&1 &
note "Read-only comparison requested; report remains on HDD ($RUN_ID)."
