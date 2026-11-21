#!/usr/bin/env bash
# Tiny, local, one-shot verdict from the two completed validation JSON reports.
# No GPU, network, training, checkpoint writes, or raw document publication.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB="posta-multi-v9-decision-20261121-v1"
if [ ! -d "$ROOT" ] || [ ! -x "$PYTHON" ] ||
   [ -e "$ROOT/$JOB.exit" ] || [ -e "$ROOT/$JOB.json" ]; then
    exit 0
fi
for f in "$ROOT/posta-multi-compare-v8-retrieval-20261121-v1.json" \
         "$ROOT/posta-multi-retrieval-adam-v9-eval.json" \
         "$ROOT/posta-multi-retrieval-adam-v9.log" \
         "$APP/research/posta_multi/checkpoint_decision.py"; do
    [ -s "$f" ] || exit 0
done
command -v timeout >/dev/null || exit 0
command -v flock >/dev/null || exit 0
# Keep failure visible but never block production auto-update.
if flock -n -E 75 "$ROOT/$JOB.lock" timeout --signal=TERM 30 \
     "$PYTHON" "$APP/research/posta_multi/checkpoint_decision.py" \
     --root "$ROOT" --output "$ROOT/$JOB.json" \
     >>"$ROOT/$JOB.log" 2>&1; then
    printf '0\n' > "$ROOT/$JOB.exit"
    printf '[posta-multi-decision] Validation verdict recorded.\n'
else
    code="$?"
    # A contended lock is not a failed evaluation: just check on next cron tick.
    if [ "$code" != '75' ]; then
        printf '%s\n' "$code" > "$ROOT/$JOB.exit"
        printf '[posta-multi-decision] Verdict failed with code %s.\n' "$code"
    fi
fi
