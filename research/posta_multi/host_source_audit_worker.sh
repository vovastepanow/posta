#!/usr/bin/env bash
# Report HF repo revision/license claim, training config/split and column types.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-source-audit-v1"
[ ! -e "$ROOT/$JOB.exit" ] && [ ! -e "$ROOT/$JOB.json" ] || exit 0
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] Source audit exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=30s 480 \
    "$PYTHON" research/pmr_datasets/probe.py --rows --strict \
    --ids fineweb-edu-en fineweb2-ir-ru fineweb2-ir-en \
    --out "$ROOT/$JOB.json"
