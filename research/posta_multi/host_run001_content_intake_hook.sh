#!/usr/bin/env bash
# Launch an isolated read-only-except-HDD source quarantine preview once.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-content-samples-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ -s "$APP/research/pmr_datasets/harvest_content_sources.py" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB" ] || exit 0
for cmd in flock nohup nice timeout; do command -v "$cmd" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_content_intake_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Bounded news and forum quarantine intake scheduled; NO training.\n'
