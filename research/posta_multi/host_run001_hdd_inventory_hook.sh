#!/usr/bin/env bash
# One-time existing-data capacity report, invoked by the CI-gated auto-updater.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-hdd-inventory-v1"
[ -d "$ROOT/downloads-v0.1" ] && [ -x "$PYTHON" ] || exit 0
[ -s "$APP/research/pmr_datasets/local_training_inventory.py" ] || exit 0
[ ! -e "$ROOT/$JOB.json" ] && [ ! -e "$ROOT/$JOB.exit" ] || exit 0
for cmd in flock nohup nice timeout; do command -v "$cmd" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_hdd_inventory_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Existing HDD data inventory launched; no download or training.\n'
