"""Bounded experimental continuation of the trained Posta Multi retrieval tower.

Runs only if an earlier read-only comparison completed successfully and both
checkpoints were evaluated on the exact same local validation pairs. Does not
promote models to production, publish data, or alter source checkpoints.
"""
import argparse
import json
import math
from pathlib import Path
import shutil
import subprocess
import sys

from compare_checkpoints import CHECKPOINTS, DATASET, ROOT, TOKENIZER

COMPARISON = ROOT / 'posta-multi-compare-v8-retrieval-20261121-v1.json'
OUTPUT = ROOT / 'posta-multi-retrieval-adam-v9.pt'
EVALUATION = ROOT / 'posta-multi-retrieval-adam-v9-eval.json'
SOURCE_NAMES = ('retrieval-v1', 'text-v8')


def choose_source(report):
    """Provisional selection heuristic: pairwise accuracy, then full-pool MRR.

    Both are tiny local validation measures, not production selection criteria.
    Refuse to choose if any comparison is missing or selection is inconsistent.
    """
    if report.get('format') != 'posta-multi-checkpoint-comparison-v1' or report.get('warnings'):
        raise ValueError('unsupported or warning-bearing comparison report')
    observations = {}
    for name in SOURCE_NAMES:
        for task in ('text', 'retrieval'):
            matches = [r for r in report.get('results', [])
                       if r.get('checkpoint') == name and r.get('task') == task]
            if len(matches) != 1 or matches[0].get('status') != 'ok':
                raise ValueError(f'incomplete or failed comparison: {name}/{task}')
            observations[(name, task)] = matches[0]['metrics']
    reference = observations[('retrieval-v1', 'retrieval')]
    challenger = observations[('text-v8', 'retrieval')]
    if (reference.get('validation_pairs_sha256') is None or
            reference.get('validation_pairs_sha256') != challenger.get('validation_pairs_sha256') or
            reference.get('samples') != challenger.get('samples') or
            reference.get('samples', 0) < 10):
        raise ValueError('validation selections differ or are insufficient')
    for row in (reference, challenger):
        for field in ('pair_accuracy', 'mean_reciprocal_rank'):
            value = row.get(field)
            if not isinstance(value, (float, int)) or not math.isfinite(value) or not 0 <= value <= 1:
                raise ValueError(f'missing or invalid {field} in comparison')
    # The text v8 model must be at least as good in both local retrieval metrics
    # to justify replacing the earlier retrieval-specialist initialization.
    if (challenger['pair_accuracy'] >= reference['pair_accuracy'] and
            challenger['mean_reciprocal_rank'] >= reference['mean_reciprocal_rank']):
        return 'text-v8'
    return 'retrieval-v1'


def run(root, comparison, dataset, tokenizer, python):
    root, comparison, dataset, tokenizer = map(Path, (root, comparison, dataset, tokenizer))
    report = json.loads(comparison.read_text(encoding='utf-8'))
    chosen = choose_source(report)
    original = root / CHECKPOINTS[chosen]
    destination = root / CHECKPOINTS['retrieval-adam-v9']
    if not original.is_file() or not tokenizer.is_file() or not (dataset / 'manifest.json').is_file():
        raise FileNotFoundError('checkpoint, tokenizer or dataset manifest missing')
    if destination.exists():
        raise FileExistsError('new checkpoint already exists; refuse to overwrite/resume automatically')
    if shutil.disk_usage(root).free < 8 * 1024 ** 3:
        raise RuntimeError('at least 8 GiB free HDD space required')
    print(json.dumps({'stage': 'retrieval-adam-v9', 'source': chosen,
                      'steps': 64, 'corpus_pairs': 512, 'optimizer': 'text-only-adamw',
                      'new_checkpoint': str(destination)}, ensure_ascii=False), flush=True)

    cmd = [str(python), str(Path(__file__).resolve().parent / 'train_retrieval.py'),
           '--dataset', str(dataset), '--tokenizer', str(tokenizer), '--init', str(original),
           '--checkpoint', str(destination), '--steps', '64', '--samples', '512',
           '--save-every', '16', '--sample-hash', '--text-only-adam',
           '--learning-rate', '0.0001']
    subprocess.run(cmd, check=True)
    # Evaluation is read-only; a failing evaluator keeps the new checkpoint and
    # leaves the old source unchanged rather than automatically promoting it.
    comparison_command = [
        str(python), str(Path(__file__).resolve().parent / 'compare_checkpoints.py'),
        '--dataset', str(dataset), '--tokenizer', str(tokenizer),
        '--checkpoint-dir', str(root), '--checkpoints', chosen, 'retrieval-adam-v9',
        '--tasks', 'text', 'retrieval',
        '--output', str(root / 'posta-multi-retrieval-adam-v9-eval.json'),
    ]
    subprocess.run(comparison_command, check=True)
    print('Experimental checkpoint completed and compared; no automatic model promotion', flush=True)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', default=str(ROOT))
    p.add_argument('--comparison', default=str(COMPARISON))
    p.add_argument('--dataset', default=str(DATASET))
    p.add_argument('--tokenizer', default=str(TOKENIZER))
    args = p.parse_args()
    run(args.root, args.comparison, args.dataset, args.tokenizer, sys.executable)


if __name__ == '__main__':
    main()
