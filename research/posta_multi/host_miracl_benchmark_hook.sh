#!/usr/bin/env bash
# One-shot human-judged RU+EN MIRACL dev evaluation, no training/DB.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-miracl-dev-400-v1"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] &&
    [ ! -e "$ROOT/$JOB.jsonl" ] || exit 0
[ -s "$APP/research/posta_multi/miracl_benchmark.py" ] || exit 0
for command in flock nohup nice timeout; do
    command -v "$command" >/dev/null || exit 0
done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_miracl_benchmark_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-miracl] Requesting evaluation-only human-judged dev benchmark.\n'
