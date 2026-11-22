#!/usr/bin/env bash
# One-time research experiment following the measured 24-step AV continuation.
# Never competes with existing CUDA jobs or changes production model weights.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB=posta-multi-av-geometry-refinement-v1
OLD=posta-multi-av-continuation-v1
SCRIPT="$APP/research/posta_multi/host_av_refinement_worker.sh"

[ -x "$PYTHON" ] && [ -f "$SCRIPT" ] && [ -d "$ROOT" ] || exit 0
[ -s "$ROOT/$OLD.exit" ] && [ "$(cat "$ROOT/$OLD.exit")" = 0 ] || exit 0
[ -s "$ROOT/$OLD.eval.exit" ] && [ "$(cat "$ROOT/$OLD.eval.exit")" = 0 ] || exit 0
[ -s "$ROOT/run001-av-continued-v1/last.pt" ] || exit 0
[ -s "$ROOT/run001-av-continued-v1/heldout-after.json" ] || exit 0
[ ! -e "$ROOT/$JOB.exit" ] || exit 0
for command in nvidia-smi flock nohup timeout nice; do
    command -v "$command" >/dev/null 2>&1 || exit 0
done
# Do not launch a second job if an external process owns substantial GPU RAM.
memory_free="$(nvidia-smi --query-gpu=memory.free --format=csv,noheader,nounits 2>/dev/null | tr -d '[:space:]')"
[[ "$memory_free" =~ ^[0-9]+$ ]] && (( memory_free >= 1500 )) || exit 0
while IFS= read -r pid; do
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    name="$(cat "/proc/$pid/comm" 2>/dev/null || echo unknown)"
    case "$name" in
        gnome-shell|Xorg|Xwayland|ptyxis|chrome|chromium|firefox|FlClashX)
            ;; # Display processes are covered by the free-memory check.
        *) printf '[posta-av-v2] GPU process %s (%s) active; skip.\n' "$pid" "$name"
           exit 0 ;;
    esac
done < <(nvidia-smi --query-compute-apps=pid --format=csv,noheader,nounits 2>/dev/null | tr -d ' ')
if ps -eo args | grep -Eq '[p]ython3 research/posta_multi/(run001_(text|retrieval)|av_235m_(eval|train_pilot|continue|refine))[.]py'; then
    exit 0
fi
umask 077
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$SCRIPT" 9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-av-v2] Queued 8-step geometry refinement behind held-out quality gate.\n'
