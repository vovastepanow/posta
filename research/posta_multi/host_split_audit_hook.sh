#!/usr/bin/env bash
# Start exactly one read-only audit after the prior diagnostic stage completes.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-retrieval-split-audit-20261121-v1"
GEOMETRY="posta-multi-geometry-v1-v8-v9-20261121-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
# The audit is CPU-only but reads the entire train/test corpus, so avoid
# competing with the earlier GPU diagnostic / HDD work.
[ -s "$ROOT/$GEOMETRY.exit" ] || exit 0
[ -f "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" ] || exit 0
[ -f "$APP/research/posta_multi/retrieval_split_audit.py" ] || exit 0
for cmd in flock nohup nice timeout; do command -v "$cmd" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_split_audit_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-multi-split-audit] Aggregate split audit requested.\n'
