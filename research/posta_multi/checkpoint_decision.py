"""Read-only validation decision for the completed Posta Multi v9 pilot.

Combines two existing 16-query validation reports that share identical
queries, corpus and tokenizer. The real initialization source is read from
the local training log. Does not load weights, call the GPU, train or promote.
"""
import argparse
import json
import math
from pathlib import Path

from compare_checkpoints import ROOT, atomic_report

INITIAL = 'posta-multi-compare-v8-retrieval-20261121-v1.json'
AFTER = 'posta-multi-retrieval-adam-v9-eval.json'
TRAIN_LOG = 'posta-multi-retrieval-adam-v9.log'
DECISION = 'posta-multi-v9-decision-20261121-v1.json'
CHECKPOINTS = ('retrieval-v1', 'text-v8', 'retrieval-adam-v9')
METRICS = ('pair_accuracy', 'recall_at_1', 'recall_at_5',
           'recall_at_10', 'mean_reciprocal_rank')


def training_source(log_path):
    source = None
    with Path(log_path).open(encoding='utf-8', errors='replace') as stream:
        for line in stream:
            try:
                row = json.loads(line)
            except ValueError:
                continue
            if not isinstance(row, dict) or row.get('stage') != 'retrieval-adam-v9':
                continue
            candidate = row.get('source')
            if candidate not in ('retrieval-v1', 'text-v8'):
                raise ValueError('training log has unsupported source')
            if source is not None and source != candidate:
                raise ValueError('training log contains conflicting initialization sources')
            source = candidate
    if source is None:
        raise ValueError('training log does not establish v9 initialization source')
    return source


def retrieve(report, required):
    if report.get('format') != 'posta-multi-checkpoint-comparison-v1':
        raise ValueError('unsupported report format')
    if report.get('warnings'):
        raise ValueError('report contains comparison warnings')
    if set(report.get('checkpoints', [])) != set(required) or        set(report.get('tasks', [])) != {'text', 'retrieval'}:
        raise ValueError('report has unexpected checkpoint/task selection')
    if len(report.get('results', [])) != 2 * len(required):
        raise ValueError('missing or duplicated evaluation rows')
    collected = {}
    for name in required:
        for task in ('text', 'retrieval'):
            matches = [r for r in report['results'] if
                       r.get('checkpoint') == name and r.get('task') == task]
            if len(matches) != 1 or matches[0].get('status') != 'ok':
                raise ValueError('incomplete or failed checkpoint evaluation')
            collected[(name, task)] = matches[0]['metrics']
    return collected


def finite_metric(row, key):
    number = row.get(key)
    if isinstance(number, bool) or not isinstance(number, (int, float)) or        not math.isfinite(number) or not 0 <= number <= 1:
        raise ValueError(f'missing or invalid metric: {key}')
    return float(number)


def analyze(before, after, source):
    if source not in ('retrieval-v1', 'text-v8'):
        raise ValueError('unknown v9 initialization source')
    if before.get('dataset') != after.get('dataset') or        before.get('tokenizer') != after.get('tokenizer') or        not before.get('dataset') or not before.get('tokenizer'):
        raise ValueError('dataset or tokenizer provenance mismatch')

    a = retrieve(before, ('retrieval-v1', 'text-v8'))
    b = retrieve(after, ('retrieval-v1', 'retrieval-adam-v9'))
    rows = {
        'retrieval-v1': a[('retrieval-v1', 'retrieval')],
        'text-v8': a[('text-v8', 'retrieval')],
        'retrieval-adam-v9': b[('retrieval-adam-v9', 'retrieval')],
    }
    anchors = (a[('retrieval-v1', 'retrieval')],
               b[('retrieval-v1', 'retrieval')])
    fingerprints = {row.get('validation_pairs_sha256') for row in (*rows.values(), *anchors)}
    counts = {row.get('samples') for row in (*rows.values(), *anchors)}
    if len(fingerprints) != 1 or None in fingerprints or        len(counts) != 1 or any(type(x) is not int or x < 10 for x in counts):
        raise ValueError('comparison used different or insufficient validation selections')
    for key in METRICS:
        if abs(finite_metric(anchors[0], key) - finite_metric(anchors[1], key)) > 1e-6:
            raise ValueError('retrieval-v1 anchor metrics disagree across reports')
    measured = {
        name: {metric: finite_metric(row, metric) for metric in METRICS}
        for name, row in rows.items()
    }
    baseline = measured[source]
    new = measured['retrieval-adam-v9']
    delta = {key: round(new[key] - baseline[key], 6) for key in METRICS}
    declined = any(delta[key] < -1e-6 for key in
                   ('pair_accuracy', 'mean_reciprocal_rank', 'recall_at_1'))
    improved = all(delta[key] >= -1e-6 for key in
                   ('pair_accuracy', 'mean_reciprocal_rank', 'recall_at_1')) and                any(delta[key] > 1e-6 for key in
                   ('pair_accuracy', 'mean_reciprocal_rank', 'recall_at_1'))
    verdict = ('validation_regression' if declined else
               'tentative_local_improvement_only' if improved else
               'no_measured_local_improvement')
    return {
        'format': 'posta-multi-v9-validation-decision-v1',
        'training_source': source,
        'validation_queries': counts.pop(),
        'evaluations': measured,
        'delta_v9_vs_initial_source': delta,
        'verdict': verdict,
        'eligible_for_production': False,
        'further_training_automatically_authorized': False,
        'notes': [
            'One 16-query local validation set, not an independent benchmark.',
            'Improvement in local evaluation cannot justify production promotion.',
            'No new training, checkpoint mutation or network upload performed.',
            'No causal attribution: initialization, optimizer and training data differ.',
        ],
    }


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root', type=Path, default=ROOT)
    p.add_argument('--output', type=Path)
    args = p.parse_args()
    root = args.root
    before = json.loads((root / INITIAL).read_text(encoding='utf-8'))
    after = json.loads((root / AFTER).read_text(encoding='utf-8'))
    source = training_source(root / TRAIN_LOG)
    result = analyze(before, after, source)
    output = args.output or root / DECISION
    if output.exists():
        raise FileExistsError('decision already exists; do not overwrite')
    atomic_report(output, result)
    print(json.dumps({
        'verdict': result['verdict'],
        'source': result['training_source'],
        'deltas': result['delta_v9_vs_initial_source'],
        'production_eligible': False,
    }, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
