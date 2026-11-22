#!/usr/bin/env bash
# Once-only, CPU-only comparison of already completed private AV evaluations.
# Never trains, reads model checkpoints, uploads data or touches production.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB=posta-multi-av-checkpoint-compare-v1
PILOT=posta-multi-av-after-retrieval-v1
OUT="$ROOT/run001-av-private-pilot-v2"
SCRIPT="$APP/research/posta_multi/av_checkpoint_compare.py"
# The existing pilot may be waiting on retrieval or may have failed. Neither
# state authorizes a comparison, and no new GPU work is started here.
[ -s "$ROOT/$PILOT.exit" ] || exit 0
[ "$(cat "$ROOT/$PILOT.exit")" = 0 ] || exit 0
for file in "$OUT/baseline.json" "$OUT/after.json" "$OUT/train.json"; do
    [ -s "$file" ] || exit 0
done
[ -f "$SCRIPT" ] && [ -x "$PYTHON" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] || exit 0
command -v flock >/dev/null || exit 0
command -v mktemp >/dev/null || exit 0
umask 077
exec 9>"$ROOT/$JOB.lock"
flock -n 9 || exit 0
[ ! -e "$ROOT/$JOB.exit" ] || exit 0
if [ -e "$OUT/comparison.json" ]; then
    printf '%s\n' 0 > "$ROOT/$JOB.exit"
    exit 0
fi
temp="$(mktemp "$OUT/.comparison.XXXXXXXX")"
trap 'rm -f "$temp"' EXIT
if "$PYTHON" "$SCRIPT" \
    --baseline "$OUT/baseline.json" --after "$OUT/after.json" \
    --train "$OUT/train.json" > "$temp" 2>> "$ROOT/$JOB.log"; then
    mv -n "$temp" "$OUT/comparison.json"
    printf '%s\n' 0 > "$ROOT/$JOB.exit"
    printf '[posta-av] Research-only AV baseline comparison saved locally.\n' >> "$ROOT/$JOB.log"
else
    printf '%s\n' 1 > "$ROOT/$JOB.exit"
    printf '[posta-av] Comparison rejected inconsistent or invalid reports.\n' >> "$ROOT/$JOB.log"
    exit 1
fi
