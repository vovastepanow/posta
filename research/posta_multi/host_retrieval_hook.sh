#!/usr/bin/env bash
# Bounded, once-only Posta Multi experiment after successful local checkpoint comparison.
# Called by the existing cron; never runs while validation is incomplete.
set -Eeuo pipefail

APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
COMPARISON="posta-multi-compare-v8-retrieval-20261121-v1"
JOB="posta-multi-retrieval-adam-v9"

note() { printf '[posta-multi-v9] %s\n' "$*"; }
if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ]; then
    note 'HDD or Python runtime unavailable; skip.'
    exit 0
fi
if [ -e "$ROOT/$JOB.exit" ]; then
    exit 0
fi
# The previous read-only evaluation is the explicit training dependency.
if [ ! -s "$ROOT/$COMPARISON.exit" ] || [ "$(cat "$ROOT/$COMPARISON.exit")" != '0' ] ||
   [ ! -s "$ROOT/$COMPARISON.json" ]; then
    exit 0
fi
for path in "$ROOT/posta-multi-retrieval-pilot-v1.pt" \
            "$ROOT/posta-multi-text-adam-v8.pt" \
            "$ROOT/tokenizer-mixed-experiment-v1/tokenizer.json" \
            "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" \
            "$APP/research/posta_multi/next_retrieval_stage.py"; do
    if [ ! -f "$path" ]; then
        note "Missing required input: $path"
        exit 0
    fi
done
# Never start an unrelated training workload or overwrite a partial v9 checkpoint.
if [ -e "$ROOT/$JOB.pt" ]; then
    note "Existing experimental checkpoint detected, refusing duplicate startup."
    exit 0
fi
if ps -eo args | grep -Eq '[r]esearch/posta_multi/train_(text|retrieval|image|media)\.py'; then
    note "Another local training process is active; retry on the next cron tick."
    exit 0
fi
for cmd in flock nohup timeout nice; do
    command -v "$cmd" >/dev/null || { note "$cmd unavailable; skip."; exit 0; }
done

# A background worker MUST close FD9 inherited from deploy/auto-update.sh,
# otherwise the training process would hold the production deployment lock.
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_retrieval_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
note "Requested one capped 64-step v9 experiment; results stay on the HDD."
