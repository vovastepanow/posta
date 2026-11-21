#!/usr/bin/env bash
# One-shot read-only test-split evaluation of v1, v8 and v9 following green deploy.
set -Eeuo pipefail

APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-holdout-v1-v8-v9-20261121-v1"

note() { printf '[posta-multi-holdout] %s\n' "$*"; }
if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ] || [ -e "$ROOT/$JOB.exit" ]; then
    exit 0
fi
# Do not run until the earlier fixed v9 pilot has fully completed.
if [ ! -s "$ROOT/posta-multi-retrieval-adam-v9.exit" ] ||
   [ "$(cat "$ROOT/posta-multi-retrieval-adam-v9.exit")" != '0' ]; then
    exit 0
fi
for path in "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" \
            "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
            "$ROOT/posta-multi-retrieval-pilot-v1.pt" \
            "$ROOT/posta-multi-text-adam-v8.pt" \
            "$ROOT/posta-multi-retrieval-adam-v9.pt" \
            "$APP/research/posta_multi/compare_checkpoints.py"; do
    if [ ! -f "$path" ]; then
        note "Missing input: $path; no evaluation started."
        exit 0
    fi
done
for cmd in flock nohup nice timeout; do
    command -v "$cmd" >/dev/null || { note "Missing $cmd; skipped."; exit 0; }
done
if ps -eo args | grep -Eq '[r]esearch/posta_multi/train_(text|retrieval|image|media)\.py'; then
    note 'Another local model trainer is active; skip until next check.'
    exit 0
fi
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_holdout_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
note "Requested one read-only test-split evaluation; report stays on HDD."
