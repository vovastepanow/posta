#!/usr/bin/env bash
# Launch real 235M AV continuation only after the private pilot and comparison.
set -Eeuo pipefail
APP="${POSTA_DEPLOY_DIR:-/var/www/html/posta}"
ROOT="${POSTA_ML_ROOT:-/mnt/hdd_ubuntu/posta-pmr}"
PYTHON="${POSTA_ML_PYTHON:-${HOME}/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3}"
JOB=posta-multi-av-continuation-v1
PILOT=posta-multi-av-after-retrieval-v1
COMPARE=posta-multi-av-checkpoint-compare-v1
RETRIEVAL=posta-multi-retrieval-expanded-resume-v2
OUT="$ROOT/run001-av-private-pilot-v2"
RUN="$ROOT/run001-av-continued-v1"
SCRIPT="$APP/research/posta_multi/av_235m_continue.py"

[ -x "$PYTHON" ] && [ -f "$SCRIPT" ] && [ -d "$ROOT" ] || exit 0
for job in "$PILOT" "$COMPARE" "$RETRIEVAL"; do
    [ -s "$ROOT/$job.exit" ] && [ "$(cat "$ROOT/$job.exit")" = 0 ] || exit 0
done
for file in "$OUT/checkpoint-step1.pt" "$OUT/train.json" "$OUT/comparison.json"; do
    [ -s "$file" ] || exit 0
done
[ ! -e "$ROOT/$JOB.exit" ] || exit 0
# Refuse to overwrite any partial output that has no resumable checkpoint.
[ ! -d "$RUN" ] || [ -s "$RUN/last.pt" ] || exit 0
for cmd in nvidia-smi flock nohup timeout nice; do
    command -v "$cmd" >/dev/null 2>&1 || exit 0
done
# Protect other GPU workloads, including user's own applications.
free="$(nvidia-smi --query-gpu=memory.free --format=csv,noheader,nounits 2>/dev/null | tr -d '[:space:]')"
[[ "$free" =~ ^[0-9]+$ ]] && (( free >= 1500 )) || exit 0
# NVIDIA's "compute-apps" list can include C+G display processes on
# desktop GPUs (for example the ptyxis terminal), even with no ML workload.
# Do not deadlock training solely because of a desktop application. We still
# enforce 1.5GiB free and reject any other compute process.
while IFS= read -r pid; do
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    executable="$(cat "/proc/$pid/comm" 2>/dev/null || echo unknown)"
    case "$executable" in
        gnome-shell|Xwayland|Xorg|ptyxis|chromium|chrome|firefox|FlClashX)
            ;; # Ordinary graphics processes are covered by the memory guard.
        *)
            printf '[posta-av] Delaying training: GPU compute process %s (%s) active.\n' "$pid" "$executable"
            exit 0
            ;;
    esac
done < <(nvidia-smi --query-compute-apps=pid --format=csv,noheader,nounits 2>/dev/null | tr -d ' ')
if ps -eo args | grep -Eq '[p]ython3 research/posta_multi/(run001_(text|retrieval)|av_235m_(eval|train_pilot|continue))[.]py'; then
    exit 0
fi
umask 077
POSTA_DEPLOY_DIR="$APP" POSTA_ML_ROOT="$ROOT" POSTA_ML_PYTHON="$PYTHON" \
    nohup flock -n "$ROOT/$JOB.lock" \
    bash "$APP/research/posta_multi/host_av_continuation_worker.sh" \
    9>&- </dev/null >>"$ROOT/$JOB.log" 2>&1 &
printf '[posta-av] Queued private 235M AV continuation.\n'
