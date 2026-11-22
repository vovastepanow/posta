#!/usr/bin/env bash
# Start the already-prepared bounded AV pilot once after the retrieval stage.
# The pilot itself enforces checkpoint provenance, idle GPU, private outputs,
# and a one-step training limit. No production model can be promoted.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
JOB=posta-multi-av-after-retrieval-v1
RETRIEVAL=posta-multi-retrieval-expanded-resume-v2
SCRIPT="$APP/research/posta_multi/queue_av_after_retrieval.sh"
SOURCE="$ROOT/run001-local-research-retrieval-expanded-v1/run001-retrieval-best.pt"
OUT="$ROOT/run001-av-private-pilot-v2"

[ -f "$SCRIPT" ] && [ -d "$ROOT" ] && [ -s "$SOURCE" ] || exit 0
[ -s "$ROOT/$RETRIEVAL.exit" ] || exit 0
[ "$(cat "$ROOT/$RETRIEVAL.exit")" = 0 ] || exit 0
# Existing output, an old final result, or a running manual/systemd launch
# means this hook must never create another training experiment.
[ ! -e "$OUT" ] && [ ! -e "$ROOT/$JOB.exit" ] || exit 0
if command -v systemctl >/dev/null 2>&1 &&
    systemctl --user is-active --quiet "$JOB" 2>/dev/null; then
    exit 0
fi
if ps -eo args | grep -Eq '[q]ueue_av_after_retrieval[.]sh|[a]v_235m_(train_pilot|eval)[.]py'; then
    exit 0
fi
for executable in flock nohup; do command -v "$executable" >/dev/null || exit 0; done

# A concurrent cron invocation may race to launch. flock is held by the
# background pilot for its entire lifetime, not just by this short hook.
umask 077
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" \
    nohup flock -n "$ROOT/$JOB.launch.lock" \
        bash "$SCRIPT" 9>&- </dev/null >/dev/null 2>&1 &
printf '[posta-av] Bounded research AV pilot queued after successful retrieval.\n'
