#!/usr/bin/env bash
# One-shot explicitly restricted 235M research pilot on EXISTING local data.
# Model and tokenizer are not approved for production, never uploaded/promoted.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-local-real-pilot-v1"
SMOKE="posta-multi-235m-run-001-seq96-smoke"
[ -d "$ROOT" ] && [ -x "$PYTHON" ] || exit 0
[ -s "$ROOT/Posta-Dataset-v0.1-screened/manifest.json" ] || exit 0
[ -s "$APP/research/posta_multi/run001_text.py" ] || exit 0
[ -s "$APP/research/posta_multi/run001_tokenizer.py" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.done" ] || exit 0
[ -s "$ROOT/$SMOKE.exit" ] && [ "$(cat "$ROOT/$SMOKE.exit")" = '0' ] || exit 0
[ -s "$ROOT/$SMOKE.json" ] || exit 0
if ps -eo args | grep -Eq '[r]esearch/posta_multi/(train_|eval_|compare_checkpoints.py|run001_.*\.py)'; then
    exit 0
fi
for cmd in flock nohup nice timeout; do command -v "$cmd" >/dev/null || exit 0; done
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_run001_real_pilot_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-run001] Private 235M research-only training pilot requested; no release.\n'
