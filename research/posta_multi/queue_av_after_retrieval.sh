#!/usr/bin/env bash
# One private research pilot, serialised after the already-running retrieval job.
# All outputs stay quarantined; no deployment or production promotion occurs.
set -Eeuo pipefail
root=/mnt/hdd_ubuntu/posta-pmr
job=posta-multi-av-after-retrieval-v1
retrieval_job=posta-multi-retrieval-expanded-resume-v2
python=/home/v0v4/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3
out="$root/run001-av-private-pilot-v2"
source="$root/run001-local-research-retrieval-expanded-v1/run001-retrieval-best.pt"
dataset="$root/Posta-Dataset-v0.1-screened"
tokenizer="$root/run001-local-research-tokenizer-v1/tokenizer.json"
umask 077
exec >>"$root/$job.log" 2>&1
finish() { code=$?; printf '%s\n' "$code" > "$root/$job.exit"; printf '[posta-av] exit=%s\n' "$code"; }
trap finish EXIT
trap 'exit 143' TERM INT
export TMPDIR="$root/tmp" PYTHONPATH="$root/torch-cu126:$root/python-libs:$root/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2 PYTHONDONTWRITEBYTECODE=1
cd /var/www/html/posta
printf '[posta-av] Waiting for retrieval training to finish.\n'
while systemctl --user is-active --quiet "$retrieval_job"; do
  if (( SECONDS > 21600 )); then echo 'Timed out waiting for retrieval'; exit 1; fi
  sleep 30
done
[ -s "$root/$retrieval_job.exit" ] && [ "$(cat "$root/$retrieval_job.exit")" = 0 ] || { echo 'Retrieval did not finish successfully'; exit 1; }
[ -s "$source" ] || { echo 'No healthy retrieval source checkpoint'; exit 1; }
if ps -eo args | grep -Eq '[p]ython3 research/posta_multi/(run001_(text|retrieval)|av_235m_(eval|train_pilot))\.py'; then
  echo 'Another Posta GPU job is active'; exit 1
fi
gpu_free=$(nvidia-smi --query-gpu=memory.free --format=csv,noheader,nounits)
[[ "$gpu_free" =~ ^[0-9]+$ ]] && (( gpu_free >= 1500 )) || { echo 'Insufficient GPU memory'; exit 1; }
while IFS= read -r pid; do
  [[ "$pid" =~ ^[0-9]+$ ]] || continue
  name=$(cat "/proc/$pid/comm" 2>/dev/null || true)
  if [[ "$name" == python* ]]; then echo 'Another Python GPU task is active'; exit 1; fi
done < <(nvidia-smi --query-compute-apps=pid --format=csv,noheader,nounits | tr -d ' ')
[ ! -e "$out" ] || { echo 'AV pilot output already exists'; exit 1; }
mkdir -m 700 "$out"
printf '[posta-av] Evaluating baseline with centered time and hierarchical residual.\n'
nice -n 10 timeout --signal=TERM --kill-after=60s 900 "$python" research/posta_multi/av_235m_eval.py \
 --dataset "$dataset" --tokenizer "$tokenizer" --checkpoint "$source" --limit 4 \
 --time-encoding relative-centered-v1 --window-aggregation hierarchical-residual-v1 > "$out/baseline.tmp"
mv "$out/baseline.tmp" "$out/baseline.json"
printf '[posta-av] Training one bounded step on real video and speech.\n'
nice -n 10 timeout --signal=TERM --kill-after=60s 900 "$python" research/posta_multi/av_235m_train_pilot.py \
 --dataset "$dataset" --tokenizer "$tokenizer" --source "$source" --output "$out/checkpoint-step1.pt" \
 --window-aggregation hierarchical-residual-v1 > "$out/train.tmp"
mv "$out/train.tmp" "$out/train.json"
printf '[posta-av] Evaluating the same held-out pool after the step.\n'
nice -n 10 timeout --signal=TERM --kill-after=60s 900 "$python" research/posta_multi/av_235m_eval.py \
 --dataset "$dataset" --tokenizer "$tokenizer" --checkpoint "$out/checkpoint-step1.pt" --limit 4 > "$out/after.tmp"
mv "$out/after.tmp" "$out/after.json"
printf '[posta-av] Pilot and paired evaluations completed; quality remains unverified.\n'
