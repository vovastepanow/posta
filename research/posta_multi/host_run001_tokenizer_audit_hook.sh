#!/usr/bin/env bash
# The existing CI-gated host updater may trigger one read-only tokenizer audit.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-tokenizer-intake-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ -f "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" ] || exit 0
[ -s "$APP/research/posta_multi/run001_tokenizer.py" ] || exit 0
[ ! -e "$ROOT/$JOB.json" ] && [ ! -e "$ROOT/$JOB.exit" ] || exit 0
for command in flock nohup nice timeout; do command -v "$command" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_tokenizer_audit_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] tokenizer data-only intake requested; no training started.\n'
