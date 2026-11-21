#!/usr/bin/env bash
# One-shot 20-row-per-source inspection preview after metadata probe success.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-source-preview-v1"
AUDIT="posta-multi-run001-source-audit-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB" ] || exit 0
[ -s "$ROOT/$AUDIT.exit" ] && [ "$(cat "$ROOT/$AUDIT.exit")" = 0 ] || exit 0
[ -s "$APP/research/pmr_datasets/sample_preview.py" ] || exit 0
for command in flock nohup timeout nice; do command -v "$command" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_source_preview_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Requesting bounded unapproved source review previews.\n'
