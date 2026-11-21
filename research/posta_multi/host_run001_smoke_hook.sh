#!/usr/bin/env bash
# One-shot synthetic GPU memory/training feasibility test for Posta Multi Run 001.
# Never loads real data or modifies checkpoints; only emits numeric JSON.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run-001-smoke"

if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ] ||
   [ -e "$ROOT/$JOB.exit" ] || [ -e "$ROOT/$JOB.json" ]; then
    exit 0
fi
for source in "$APP/research/posta_multi/run001_smoke.py" \
              "$APP/research/posta_multi/model.py"; do
    [ -s "$source" ] || exit 0
done
# Run only after earlier HDD comparisons have finished, regardless of outcome.
for previous in "posta-multi-holdout-v1-v8-v9-20261121-v1" \
                "posta-multi-geometry-v1-v8-v9-20261121-v1"; do
    [ -s "$ROOT/$previous.exit" ] || exit 0
done
# Avoid overlapping any existing model run or evaluator.
if ps -eo args | grep -Eq '[r]esearch/posta_multi/(train_|eval_|compare_checkpoints.py|run001_smoke.py)'; then
    exit 0
fi
for command in flock nohup nice timeout; do
    command -v "$command" >/dev/null || exit 0
done
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_smoke_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] One-shot synthetic GPU step requested; no real-data training.\n'
