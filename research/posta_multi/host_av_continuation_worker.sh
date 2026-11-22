#!/usr/bin/env bash
# Bounded, resumable real training; writes only private HDD checkpoints.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB=posta-multi-av-continuation-v1
PILOT="$ROOT/run001-av-private-pilot-v2"
RUN="$ROOT/run001-av-continued-v1"
DATASET="$ROOT/Posta-Dataset-v0.1-screened"
TOKENIZER="$ROOT/run001-local-research-tokenizer-v1/tokenizer.json"
STEPS=24
MAX_ATTEMPTS=5
umask 077
mkdir -p "$ROOT/tmp"
[ ! -e "$ROOT/$JOB.exit" ] || exit 0
attempts=0
if [ -f "$ROOT/$JOB.attempts" ]; then
    attempts="$(cat "$ROOT/$JOB.attempts")"
fi
if ! [[ "$attempts" =~ ^[0-9]+$ ]] || (( attempts >= MAX_ATTEMPTS )); then
    echo 1 > "$ROOT/$JOB.exit"
    echo "[posta-av] Resume attempt budget exhausted or invalid"
    exit 1
fi
printf '%s\n' "$((attempts + 1))" > "$ROOT/$JOB.attempts"
export TMPDIR="$ROOT/tmp"
export PYTHONPATH="$ROOT/torch-cu126:$ROOT/python-libs:$ROOT/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2 PYTHONDONTWRITEBYTECODE=1
cd "$APP"
if nice -n 10 timeout --signal=TERM --kill-after=90s 21600 \
  "$PYTHON" research/posta_multi/av_235m_continue.py \
    --dataset "$DATASET" \
    --tokenizer "$TOKENIZER" \
    --source "$PILOT/checkpoint-step1.pt" \
    --pilot-summary "$PILOT/train.json" \
    --comparison "$PILOT/comparison.json" \
    --output "$RUN" --steps "$STEPS" --save-every 2 --lr 0.000005; then
    echo 0 > "$ROOT/$JOB.exit"
    echo "[posta-av] Completed $STEPS optimizer steps; weights remain private and unpromoted"
else
    code=$?
    # Exit 124 means the bounded window ended normally. The atomic last.pt
    # can be resumed by the next cron tick if it exists; never replay from
    # the uncheckpointed source after an interrupted first step.
    if (( code == 124 )) && [ -s "$RUN/last.pt" ] && (( attempts + 1 < MAX_ATTEMPTS )); then
        echo "[posta-av] Bounded training window ended; next cron will resume last.pt"
        exit 0
    fi
    echo "$code" > "$ROOT/$JOB.exit"
    echo "[posta-av] Continuation failed with exit $code; see private log"
    exit "$code"
fi
