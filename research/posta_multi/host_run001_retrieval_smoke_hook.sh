#!/usr/bin/env bash
# Exactly one full 235M synthetic GPU anti-collapse step, after seq96 success.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run-001-retrieval-smoke"
PRIOR="posta-multi-235m-run-001-seq96-smoke"
if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ] ||
   [ -e "$ROOT/$JOB.exit" ] || [ -e "$ROOT/$JOB.json" ]; then exit 0; fi
[ -s "$ROOT/$PRIOR.exit" ] &&
    [ "$(cat "$ROOT/$PRIOR.exit")" = '0' ] &&
    [ -s "$ROOT/$PRIOR.json" ] || exit 0
[ -s "$APP/research/posta_multi/run001_retrieval_smoke.py" ] || exit 0
if ps -eo args | grep -Eq '[r]esearch/posta_multi/(train_|eval_|compare_checkpoints.py|run001_.*\.py)'; then
    exit 0
fi
for tool in flock nohup nice timeout; do command -v "$tool" >/dev/null || exit 0; done
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_retrieval_smoke_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Requested one synthetic anti-collapse 235M GPU step.\n'
