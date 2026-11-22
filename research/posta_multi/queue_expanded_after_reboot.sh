#!/usr/bin/env bash
set -Eeuo pipefail
root=/mnt/hdd_ubuntu/posta-pmr
python=/home/v0v4/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3
job=posta-multi-retrieval-expanded-resume-v2
text_job=posta-multi-text-expanded-resume-v2
out="$root/run001-local-research-retrieval-expanded-v1"
umask 077
exec >>"$root/$job.log" 2>&1
finish() { code=$?; printf '%s\n' "$code" > "$root/$job.exit"; printf '[posta-run001] expanded retrieval exit=%s\n' "$code"; }
trap finish EXIT
trap 'exit 143' TERM INT
export TMPDIR="$root/tmp" PYTHONPATH="$root/torch-cu126:$root/python-libs:$root/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd /var/www/html/posta
printf '[posta-run001] Waiting for resumed text phase.\n'
while systemctl --user is-active --quiet posta-multi-text-expanded-resume-v2; do
  if (( SECONDS > 23400 )); then echo 'Timed out waiting for text'; exit 1; fi
  sleep 30
done
[ -s "$root/$text_job.exit" ] && [ "$(cat "$root/$text_job.exit")" = 0 ] || { echo 'Text did not finish successfully'; exit 1; }
"$python" - "$root/run001-local-research-235m-v1/run001-text-last.pt" <<'PY'
import sys, torch
s = torch.load(sys.argv[1], map_location='cpu', weights_only=True, mmap=True)
k = s.get('settings', {}); step = s.get('step'); stale = s.get('stale')
if (s.get('format') != 'posta-multi-235m-run001-text-v3' or type(step) is not int or
    k.get('steps') != 3000 or k.get('train_per_group') != 8000 or
    k.get('quarantined_research_only') is not True or k.get('private_local_only') is not True or
    k.get('production_promotion_allowed') is not False or
    not (step == 3000 or (1000 < step < 3000 and type(stale) is int and stale >= k.get('patience', 10**9)))):
    raise SystemExit('Text checkpoint was not completed or validly early-stopped')
print('Text checkpoint verified at step', step, flush=True)
PY
if ps -eo args | grep -Eq '[p]ython3 research/posta_multi/run001_(text|retrieval|compare_validation|hybrid_validation)\.py'; then
  echo 'Another Run 001 job is active; retrieval skipped'; exit 1
fi
[ ! -e "$out" ] || { echo 'Output already exists'; exit 1; }
mkdir -m 700 "$out"
ln "$root/run001-local-research-235m-v1/run001-text-best.pt" "$out/stage1-source.pt"
nice -n 10 timeout --signal=TERM --kill-after=90s 21600 "$python" research/posta_multi/run001_retrieval.py \
 --dataset "$root/Posta-Dataset-v0.1-screened" --tokenizer "$root/run001-local-research-tokenizer-v1/tokenizer.json" \
 --init "$out/stage1-source.pt" --output "$out" --local-research-quarantine \
 --steps 500 --batch-size 4 --train-pairs 4096 --val-pairs 64 --max-length 128 \
 --projection-only-steps 25 --positive-only-pairs 1024 --video-caption-pairs 512 --video-caption-batch 2 --video-caption-every 8 --video-caption-weight 0.1 --validate-every 25 --save-every 25 --patience 6
