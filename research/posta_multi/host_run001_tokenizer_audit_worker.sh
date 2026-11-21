#!/usr/bin/env bash
# One-shot report-only tokenizer intake on the existing local candidate.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-run001-tokenizer-intake-v1"
REPORT="$ROOT/$JOB.json"
EXIT="$ROOT/$JOB.exit"
[ ! -e "$REPORT" ] && [ ! -e "$EXIT" ] || exit 0
TEMP="$ROOT/.$JOB.$$.tmp"
umask 077
finish() {
    code="$?"
    rm -f "$TEMP"
    printf '%s\n' "$code" > "$EXIT"
    printf '[posta-run001] tokenizer intake exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp" OMP_NUM_THREADS=2 MKL_NUM_THREADS=2
cd "$APP"
nice -n 10 timeout --signal=TERM --kill-after=20s 900 \
    "$PYTHON" research/posta_multi/run001_tokenizer.py \
    --dataset "$ROOT/Posta-Dataset-v0.1-screened" \
    --languages ru,en --audit-only > "$TEMP"
mv -n "$TEMP" "$REPORT"
