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
    # Evaluate the frozen held-out set only after the training process has
    # exited and released GPU memory. A failed evaluation never reruns training.
    if [ ! -e "$ROOT/$JOB.eval.exit" ]; then
        if nice -n 10 timeout --signal=TERM --kill-after=90s 3600 \
            "$PYTHON" research/posta_multi/av_235m_eval.py \
              --dataset "$DATASET" --tokenizer "$TOKENIZER" \
              --checkpoint "$RUN/last.pt" --limit 4 \
              --time-encoding relative-centered-v1 \
              --window-aggregation hierarchical-residual-v1 \
              > "$RUN/heldout-after.tmp"; then
            mv "$RUN/heldout-after.tmp" "$RUN/heldout-after.json"
            printf '0\n' > "$ROOT/$JOB.eval.exit"
            echo "[posta-av] Held-out continuation evaluation saved on HDD"
        else
            code=$?
            rm -f "$RUN/heldout-after.tmp"
            printf '%s\n' "$code" > "$ROOT/$JOB.eval.exit"
            echo "[posta-av] Training succeeded but evaluation failed with exit $code"
        fi
    fi
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
