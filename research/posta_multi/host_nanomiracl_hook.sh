#!/usr/bin/env bash
# One-shot source-derived human qrel evaluation with full Nano corpus.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-nanomiracl-ru-en-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
[ -s "$APP/research/posta_multi/nanomiracl_benchmark.py" ] || exit 0
for cmd in flock nohup nice timeout; do command -v "$cmd" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_nanomiracl_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] NanoMIRACL full supplied corpus BM25 evaluation requested.\n'
