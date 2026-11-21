#!/usr/bin/env bash
# Train a REAL 235M masked-LM pilot with existing quarantined texts.
# This is a nonproduction, private experiment. It is NOT permission-clearance.
# It never touches production PostgreSQL, promotes a model, or downloads data.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-235m-local-real-pilot-v1"
DATA="$ROOT/Posta-Dataset-v0.1-screened"
TOKENIZER="$ROOT/run001-local-research-tokenizer-v1"
WEIGHTS="$ROOT/run001-local-research-235m-v1"
umask 077
finish() {
    code="$?"
    printf '%s\n' "$code" > "$ROOT/$JOB.exit"
    printf '[posta-run001] Private real-data 235M experiment exit=%s\n' "$code"
}
trap finish EXIT
mkdir -p "$ROOT/tmp"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd "$APP"
if [ ! -s "$TOKENIZER/tokenizer.json" ]; then
    echo '[posta-run001] Fitting research-only balanced RU/EN tokenizer from existing local split'
    nice -n 10 timeout --signal=TERM --kill-after=60s 3600 \
        "$PYTHON" research/posta_multi/run001_tokenizer.py \
        --dataset "$DATA" --output "$TOKENIZER" --languages ru,en \
        --local-research-quarantine
fi
[ -s "$TOKENIZER/metadata.json" ] || exit 1
echo '[posta-run001] Starting actual gradient updates on real local texts, NOT synthetic IDs'
resume=()
if [ -s "$WEIGHTS/run001-text-last.pt" ]; then
    resume=(--resume)
fi
nice -n 10 timeout --signal=TERM --kill-after=90s 14400 \
    "$PYTHON" research/posta_multi/run001_text.py \
    --dataset "$DATA" --tokenizer "$TOKENIZER/tokenizer.json" \
    --output "$WEIGHTS" \
    --local-research-quarantine \
    --steps 12 --warmup 1 --grad-accum 1 \
    --validate-every 3 --save-every 1 --patience 4 \
    "${resume[@]}"
test -s "$WEIGHTS/run001-text-last.pt"
test -s "$WEIGHTS/run001-text-best.pt"
printf '%s\n' '12-step real-data research pilot completed; weights are restricted to private research.' > "$ROOT/$JOB.done"
