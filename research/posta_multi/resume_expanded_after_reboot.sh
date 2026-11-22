#!/usr/bin/env bash
set -Eeuo pipefail
root=/mnt/hdd_ubuntu/posta-pmr
job=posta-multi-text-expanded-resume-v2
umask 077
exec >>"$root/$job.log" 2>&1
finish() { code=$?; printf '%s\n' "$code" > "$root/$job.exit"; printf '[posta-run001] resumed text exit=%s\n' "$code"; }
trap finish EXIT
trap 'exit 143' TERM INT
export TMPDIR="$root/tmp" PYTHONPATH="$root/torch-cu126:$root/python-libs:$root/tokenizer-libs"
export OMP_NUM_THREADS=2 MKL_NUM_THREADS=2 OPENBLAS_NUM_THREADS=2
cd /var/www/html/posta
nice -n 10 timeout --signal=TERM --kill-after=90s 21600 /home/v0v4/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 research/posta_multi/run001_text.py \
 --dataset "$root/Posta-Dataset-v0.1-screened" --tokenizer "$root/run001-local-research-tokenizer-v1/tokenizer.json" \
 --output "$root/run001-local-research-235m-v1" --local-research-quarantine \
 --steps 3000 --warmup 1 --grad-accum 4 --validate-every 50 --save-every 25 --patience 6 --train-per-group 8000 --resume
