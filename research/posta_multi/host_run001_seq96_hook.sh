#!/usr/bin/env bash
# One-shot full-length synthetic GPU stress test, only if short Run 001 passed.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-run-001-seq96-smoke"
PREVIOUS="posta-multi-235m-run-001-smoke"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
[ -s "$ROOT/$PREVIOUS.exit" ] && [ "$(cat "$ROOT/$PREVIOUS.exit")" = '0' ] || exit 0
[ -s "$ROOT/$PREVIOUS.json" ] || exit 0
[ -s "$APP/research/posta_multi/run001_smoke.py" ] || exit 0
if ps -eo args | grep -Eq '[r]esearch/posta_multi/(train_|eval_|compare_checkpoints.py|run001_smoke.py)'; then
    exit 0
fi
for cmd in flock nohup timeout nice; do command -v "$cmd" >/dev/null || exit 0; done
POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" POSTA_DEPLOY_DIR="$APP" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_seq96_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Requested full-length, eight-mask synthetic GPU stress test.\n'
