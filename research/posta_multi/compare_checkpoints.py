"""Read-only, sequential comparison of Posta Multi checkpoint validation metrics.

Uses existing local evaluation scripts. Never trains, writes checkpoints, changes
the corpus, or uploads media. Reports are kept on the HDD by default.
"""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path('/mnt/hdd_ubuntu/posta-pmr')
DATASET = ROOT / 'Posta-Dataset-v0.1-screened'
TOKENIZER = ROOT / 'tokenizer-mixed-experiment-v1' / 'tokenizer.json'
CHECKPOINTS = {
    'mixed-v2': 'posta-multi-mixed-pilot-v2.pt',
    'retrieval-v1': 'posta-multi-retrieval-pilot-v1.pt',
    'image-v1': 'posta-multi-image-pilot-v1.pt',
    'audio-v7': 'posta-multi-audio-expanded-v7.pt',
    'text-v8': 'posta-multi-text-adam-v8.pt',
}
DEFAULT_CHECKPOINTS = tuple(CHECKPOINTS)
TASK_OPTIONS = {
    'text': ('eval_text.py', '--per-group', '8'),
    'retrieval': ('eval_retrieval.py', '--samples', '16'),
    'image': ('eval_image.py', '--samples', '8'),
    'video': ('eval_video.py', '--samples', '8'),
    'audio': ('eval_audio.py', '--samples', '16'),
}
DEFAULT_TASKS = ('text', 'retrieval')


def evaluation_command(task, checkpoint, dataset, tokenizer, python=sys.executable):
    if task not in TASK_OPTIONS:
        raise ValueError('unknown evaluation task')
    script, option, count = TASK_OPTIONS[task]
    return [
        str(python), str(Path(__file__).resolve().parent / script),
        '--dataset', str(dataset), '--tokenizer', str(tokenizer),
        '--checkpoint', str(checkpoint), option, count,
    ]


def parse_evaluation_output(output):
    for line in reversed(output.strip().splitlines()):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict) and 'checkpoint_step' in value:
            return value
    raise ValueError('evaluation produced no checkpoint_step JSON record')


def summarize(records):
    """Report original observations only; never automatically declare a winner."""
    notes = []
    for task in TASK_OPTIONS:
        successful = [record for record in records
                      if record['task'] == task and record['status'] == 'ok']
        if len(successful) <= 1:
            continue
        digest_key = {
            'retrieval': 'validation_pairs_sha256',
            'image': 'validation_sha256',
            'video': 'validation_sha256',
            'audio': 'selection_sha256',
        }.get(task)
        if digest_key and len({r['metrics'].get(digest_key) for r in successful}) != 1:
            notes.append(f'{task}: validation selections differ; do not compare scores')
    return notes


def atomic_report(path, payload):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp = tempfile.mkstemp(prefix='.posta-multi-eval-', suffix='.json', dir=path.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(payload, stream, ensure_ascii=False, indent=2)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temp, path)
    finally:
        if os.path.exists(temp):
            os.unlink(temp)


def run(args):
    if args.timeout < 30:
        raise ValueError('timeout must be at least 30 seconds')
    dataset, tokenizer = Path(args.dataset), Path(args.tokenizer)
    if not (dataset / 'manifest.json').is_file() or not tokenizer.is_file():
        raise FileNotFoundError('dataset manifest or tokenizer is missing')
    selected = [(name, Path(args.checkpoint_dir) / CHECKPOINTS[name]) for name in args.checkpoints]
    missing = [name for name, path in selected if not path.is_file()]
    if missing:
        raise FileNotFoundError('checkpoint files missing: ' + ', '.join(missing))
    records = []
    for name, path in selected:
        for task in args.tasks:
            command = evaluation_command(task, path, dataset, tokenizer)
            print(f'[check] {name} / {task}', flush=True)
            try:
                result = subprocess.run(command, text=True, capture_output=True, timeout=args.timeout, check=False)
                if result.returncode:
                    raise RuntimeError((result.stderr or result.stdout)[-1500:].strip() or
                                       f'evaluator exited with {result.returncode}')
                metrics = parse_evaluation_output(result.stdout)
                record = {'checkpoint': name, 'task': task, 'status': 'ok', 'metrics': metrics}
                metric = metrics.get('pair_accuracy', metrics.get('mean_reciprocal_rank', 'text metrics'))
                print(f'[ok] {name} / {task}: {metric}', flush=True)
            except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as error:
                record = {'checkpoint': name, 'task': task, 'status': 'error', 'error': str(error)[-1500:]}
                print(f'[error] {name} / {task}: {record["error"]}', file=sys.stderr, flush=True)
            records.append(record)
            # Preserve partial observations even if a later evaluation is interrupted.
            payload = {
                'format': 'posta-multi-checkpoint-comparison-v1',
                'created_at_utc': datetime.now(timezone.utc).isoformat(),
                'dataset': str(dataset), 'tokenizer': str(tokenizer),
                'tasks': args.tasks, 'checkpoints': args.checkpoints,
                'results': records, 'warnings': summarize(records),
                'limitations': [
                    'Local validation is not an independent Posta benchmark',
                    'Scores from different tasks/losses are not directly comparable',
                    'Incomplete relevance labels can underestimate retrieval quality',
                    'No training, checkpoint mutation or media upload performed',
                ],
            }
            atomic_report(args.output, payload)
    return 0 if all(row['status'] == 'ok' for row in records) and not payload['warnings'] else 1


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', default=str(DATASET))
    p.add_argument('--tokenizer', default=str(TOKENIZER))
    p.add_argument('--checkpoint-dir', default=str(ROOT))
    p.add_argument('--checkpoints', nargs='+', choices=tuple(CHECKPOINTS), default=list(DEFAULT_CHECKPOINTS))
    p.add_argument('--tasks', nargs='+', choices=tuple(TASK_OPTIONS), default=list(DEFAULT_TASKS))
    p.add_argument('--timeout', type=int, default=900, help='seconds allowed per checkpoint/task')
    p.add_argument('--output', default=str(ROOT / 'posta-multi-comparison-v1.json'))
    args = p.parse_args()
    raise SystemExit(run(args))


if __name__ == '__main__':
    main()
