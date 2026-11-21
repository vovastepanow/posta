"""Read Posta Multi saved checkpoints and local validation summaries without loading weights.

This only reads tiny log/JSON files from the HDD. It never runs training or
evaluators, modifies checkpoints, or uploads private source content.
"""
import json
from pathlib import Path

ROOT = Path('/mnt/hdd_ubuntu/posta-pmr')

STAGES = (
    'posta-multi-mixed-pilot-v2',
    'posta-multi-retrieval-pilot-v1',
    'posta-multi-image-pilot-v1',
    'posta-multi-audio-full-v3',
    'posta-multi-audio-full-v4',
    'posta-multi-audio-batch4-v5',
    'posta-multi-audio-adam-v6',
    'posta-multi-audio-expanded-v7',
    'posta-multi-text-adam-v8',
    'posta-multi-retrieval-adam-v9',
)

JOB_REPORT_FILES = {
    'posta-multi-compare-v8-retrieval-20261121-v1':
        'posta-multi-compare-v8-retrieval-20261121-v1.json',
    'posta-multi-retrieval-adam-v9':
        'posta-multi-retrieval-adam-v9-eval.json',
    'posta-multi-holdout-v1-v8-v9-20261121-v1':
        'posta-multi-holdout-v1-v8-v9-20261121-v1.json',
    'posta-multi-geometry-v1-v8-v9-20261121-v1':
        'posta-multi-geometry-v1-v8-v9-20261121-v1.json',
}

RANKING_FIELDS = (
    'samples', 'checkpoint_step', 'candidate_pool_size', 'pair_accuracy',
    'recall_at_1', 'recall_at_5', 'recall_at_10', 'mean_reciprocal_rank',
)


def summarize_report(path):
    """Return small numeric metrics only, never source records or raw text."""
    path = Path(path)
    if not path.is_file():
        return None
    try:
        payload = json.loads(path.read_text(encoding='utf-8'))
    except (OSError, ValueError) as error:
        return {'read_error': str(error)}
    if not isinstance(payload, dict) or payload.get('format') != 'posta-multi-checkpoint-comparison-v1':
        return {'read_error': 'unknown comparison report format'}

    results = []
    for row in payload.get('results', []):
        if not isinstance(row, dict):
            continue
        entry = {
            'checkpoint': row.get('checkpoint'),
            'task': row.get('task'),
            'status': row.get('status'),
        }
        if row.get('status') == 'ok':
            metrics = row.get('metrics') or {}
            if entry['task'] in ('retrieval', 'retrieval-test', 'retrieval-health'):
                entry.update({key: metrics[key] for key in RANKING_FIELDS if key in metrics})
                if entry['task'] == 'retrieval-health':
                    diag = metrics.get('diagnostics', {})
                    entry['diagnostics'] = {key: value for key, value in diag.items()
                                            if key != 'note' and isinstance(value, (int, float))}
            elif entry['task'] == 'text':
                entry['checkpoint_step'] = metrics.get('checkpoint_step')
                groups = metrics.get('local_validation') or {}
                entry['text_groups'] = {
                    group: {key: values[key]
                            for key in ('records', 'masked_tokens', 'mean_loss', 'token_accuracy')
                            if key in values}
                    for group, values in sorted(groups.items())
                    if isinstance(values, dict)
                }
            else:
                entry.update({key: metrics[key] for key in RANKING_FIELDS if key in metrics})
        else:
            entry['error'] = str(row.get('error', 'unknown evaluation error'))[-300:]
        results.append(entry)

    expected = len(payload.get('tasks') or []) * len(payload.get('checkpoints') or [])
    return {
        'complete': bool(expected) and expected == len(results) and
            all(row.get('status') == 'ok' for row in results),
        'warnings': payload.get('warnings') or [],
        'results': results,
    }


def job_status(root, name):
    root = Path(root)
    exit_path = root / (name + '.exit')
    report_path = root / JOB_REPORT_FILES[name]
    exit_code = None
    if exit_path.is_file():
        try:
            exit_code = int(exit_path.read_text(encoding='utf-8').strip())
        except (ValueError, OSError):
            exit_code = 'unreadable'
    return {
        'job': name,
        'attempted': exit_path.exists(),
        'exit_code': exit_code,
        'report_exists': report_path.is_file(),
        'report_filename': report_path.name,
        'log_exists': (root / (name + '.log')).is_file(),
    }


def recent_stage(root, name):
    path = Path(root) / (name + '.log')
    checkpoint = Path(root) / (name + '.pt')
    last = None
    origin = None
    if path.exists():
        with path.open(encoding='utf-8', errors='replace') as stream:
            for line in stream:
                try:
                    value = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if not isinstance(value, dict):
                    continue
                if value.get('stage') == 'retrieval-adam-v9':
                    origin = value.get('source')
                if 'saved_step' in value or 'step' in value:
                    last = value
    result = {'stage': name, 'latest_log_entry': last,
              'checkpoint_exists': checkpoint.is_file()}
    if origin:
        result['initialized_from'] = origin
    return result


def main():
    for name in STAGES:
        print(json.dumps(recent_stage(ROOT, name), ensure_ascii=False))
    for name, filename in JOB_REPORT_FILES.items():
        print(json.dumps(job_status(ROOT, name), ensure_ascii=False))
        summary = summarize_report(ROOT / filename)
        if summary is not None:
            print(json.dumps({'report_for': name, **summary}, ensure_ascii=False))
    audit_name = 'posta-multi-retrieval-split-audit-20261121-v1'
    audit_path = ROOT / (audit_name + '.json')
    audit_exit = ROOT / (audit_name + '.exit')
    if audit_path.is_file():
        try:
            audit = json.loads(audit_path.read_text(encoding='utf-8'))
            if audit.get('format') != 'posta-retrieval-split-audit-v1':
                raise ValueError('unexpected audit schema')
            summary = {
                'split_rows': {split: value['rows'] for split, value in audit['splits'].items()},
                'without_negatives': {split: value['without_explicit_negatives']
                                      for split, value in audit['splits'].items()},
                'cross_split_overlap': audit['exact_cross_split_overlap'],
            }
        except (ValueError, KeyError, OSError, TypeError) as error:
            summary = {'read_error': str(error)}
        print(json.dumps({'split_audit': audit_name, 'exit_exists': audit_exit.is_file(),
                          'summary': summary}, ensure_ascii=False))
    else:
        print(json.dumps({'split_audit': audit_name, 'exit_exists': audit_exit.is_file(),
                          'report_exists': False}, ensure_ascii=False))
    decision = ROOT / 'posta-multi-v9-decision-20261121-v1.json'
    if decision.is_file():
        try:
            record = json.loads(decision.read_text(encoding='utf-8'))
            if record.get('format') != 'posta-multi-v9-validation-decision-v1':
                raise ValueError('invalid decision format')
            summary = {
                'source': record['training_source'],
                'verdict': record['verdict'],
                'delta_v9_vs_initial_source': record['delta_v9_vs_initial_source'],
                'eligible_for_production': record['eligible_for_production'],
            }
        except (ValueError, KeyError, OSError, TypeError) as error:
            summary = {'read_error': str(error)}
        print(json.dumps({'checkpoint_decision': summary}, ensure_ascii=False))
    else:
        print(json.dumps({'checkpoint_decision': {'report_exists': False}}, ensure_ascii=False))
    smoke_job = 'posta-multi-235m-run-001-smoke'
    smoke_report = ROOT / (smoke_job + '.json')
    smoke_exit = ROOT / (smoke_job + '.exit')
    if smoke_report.is_file():
        try:
            payload = json.loads(smoke_report.read_text(encoding='utf-8'))
            if payload.get('format') != 'posta-multi-235m-run001-smoke-v1':
                raise ValueError('unsupported Run 001 report')
            result = {
                'architecture_params': payload['architecture_params'],
                'trainable_text_params': payload['trainable_text_params'],
                'cuda_peak_allocated_mib': payload['cuda_peak_allocated_mib'],
                'synthetic_training': payload['synthetic_training'],
                'trained_on_real_data': payload['trained_on_real_data'],
            }
        except (ValueError, KeyError, TypeError, OSError) as error:
            result = {'read_error': str(error)}
    else:
        result = {'report_exists': False}
    print(json.dumps({'run001_smoke': result, 'exit_exists': smoke_exit.is_file()},
                     ensure_ascii=False))
    preflight = 'posta-multi-235m-run-001-preflight'
    preflight_report = ROOT / (preflight + '.json')
    preflight_exit = ROOT / (preflight + '.exit')
    if preflight_report.is_file():
        try:
            payload = json.loads(preflight_report.read_text(encoding='utf-8'))
            if payload.get('format') != 'posta-multi-run001-readiness-v1':
                raise ValueError('unrecognized Run 001 preflight report')
            result = {
                'allowed_to_train': payload['allowed_to_train'],
                'dataset_approval_issues': payload['dataset_approval_issues'],
                'cuda_available': payload['cuda_available'],
                'tokenizer_vocab_size': payload['tokenizer_vocab_size'],
                'model_parameters': payload['model_parameters'],
            }
        except (KeyError, ValueError, TypeError, OSError) as error:
            result = {'read_error': str(error)}
    else:
        result = {'report_exists': False}
    print(json.dumps({'run001_preflight': result, 'exit_exists': preflight_exit.is_file()},
                     ensure_ascii=False))
    seq96 = 'posta-multi-235m-run-001-seq96-smoke'
    seq96_path = ROOT / (seq96 + '.json')
    if seq96_path.is_file():
        try:
            report = json.loads(seq96_path.read_text(encoding='utf-8'))
            if report.get('format') != 'posta-multi-235m-run001-smoke-v1':
                raise ValueError('unknown seq96 smoke format')
            result = {k: report[k] for k in (
                'sequence_length', 'masked_positions',
                'cuda_peak_allocated_mib', 'synthetic_training',
                'trained_on_real_data')}
        except (ValueError, KeyError, OSError, TypeError) as error:
            result = {'read_error': str(error)}
    else:
        result = {'report_exists': False}
    print(json.dumps({'run001_seq96': result,
                      'exit_exists': (ROOT / (seq96 + '.exit')).is_file()},
                     ensure_ascii=False))
    retr_smoke = 'posta-multi-235m-run-001-retrieval-smoke'
    retr_path = ROOT / (retr_smoke + '.json')
    if retr_path.is_file():
        try:
            payload = json.loads(retr_path.read_text(encoding='utf-8'))
            if payload.get('format') != 'posta-multi-235m-run001-retrieval-smoke-v1':
                raise ValueError('unknown Run 001 retrieval smoke schema')
            summary = {k: payload[k] for k in (
                'architecture_params', 'text_trainable_params', 'sequence_length',
                'explicit_triplets', 'loss', 'cuda_peak_allocated_mib',
                'updated_projection', 'trained_on_real_data', 'search_quality_verified')}
        except (KeyError, ValueError, OSError, TypeError) as error:
            summary = {'read_error': str(error)}
    else:
        summary = {'report_exists': False}
    print(json.dumps({'run001_retrieval_smoke': summary,
                      'exit_exists': (ROOT / (retr_smoke + '.exit')).is_file()},
                     ensure_ascii=False))
    bm25_job = 'posta-multi-235m-run001-bm25-validation-v1'
    bm25_file = ROOT / (bm25_job + '.json')
    if bm25_file.is_file():
        try:
            record = json.loads(bm25_file.read_text(encoding='utf-8'))
            if record.get('format') != 'posta-multi-bm25-internal-validation-v1':
                raise ValueError('unknown BM25 benchmark schema')
            data = {
                label: {key: record[label][key] for key in (
                    'samples', 'candidate_pool_size',
                    'validation_pairs_sha256', 'pair_accuracy',
                    'recall_at_1', 'recall_at_5', 'mean_reciprocal_rank')}
                for label in ('legacy_head16', 'stable_hash')
            }
        except (KeyError, ValueError, OSError, TypeError) as error:
            data = {'read_error': str(error)}
    else:
        data = {'report_exists': False}
    print(json.dumps({'run001_bm25_validation': data,
                      'exit_exists': (ROOT / (bm25_job + '.exit')).is_file()},
                     ensure_ascii=False))
    miracl_name = 'posta-multi-nanomiracl-ru-en-v1'
    miracl_file = ROOT / (miracl_name + '.json')
    if miracl_file.is_file():
        try:
            report = json.loads(miracl_file.read_text(encoding='utf-8'))
            if report.get('format') != 'posta-nanomiracl-ru-en-full-nano-corpus-bm25-v1':
                raise ValueError('unexpected MIRACL benchmark schema')
            data = {
                'miracl_derived_queries': report['overall']['queries'],
                'bm25_mrr': report['overall']['mrr'],
                'bm25_ndcg_10': report['overall']['ndcg_10'],
                'language_breakdown': {
                    name: {'queries': v['queries'], 'mrr': v['mrr'],
                           'ndcg_10': v['ndcg_10']}
                    for name, v in report['languages'].items()},
                'full_original_miracl_corpus_retrieval': False,
            }
        except (OSError, KeyError, ValueError, TypeError) as error:
            data = {'read_error': str(error)}
    else:
        data = {'report_exists': False}
    print(json.dumps({'miracl_human_benchmark': data,
                      'exit_exists': (ROOT / (miracl_name + '.exit')).is_file()},
                     ensure_ascii=False))
    source_audit = 'posta-multi-run001-source-audit-v1'
    source_file = ROOT / (source_audit + '.json')
    if source_file.is_file():
        try:
            source = json.loads(source_file.read_text(encoding='utf-8'))
            details = [{
                'id': entry['id'], 'revision': entry.get('revision'),
                'metadata_status': entry['metadata_status'],
                'schema_status': entry['schema_status'],
                'license_card': entry.get('license_from_hf_card'),
                'review_required': entry.get('review_required', True)}
                for entry in source['datasets']]
        except (KeyError, ValueError, TypeError, OSError) as error:
            details = {'read_error': str(error)}
    else:
        details = {'report_exists': False}
    print(json.dumps({'run001_external_source_audit': details,
                      'exit_exists': (ROOT / (source_audit + '.exit')).is_file()},
                     ensure_ascii=False))
    preview_name = 'posta-multi-run001-source-preview-v1'
    preview_manifest = ROOT / preview_name / 'manifest.json'
    if preview_manifest.is_file():
        try:
            preview = json.loads(preview_manifest.read_text(encoding='utf-8'))
            entries = [{
                'source': x['id'], 'records': x.get('records'),
                'error': x.get('error'),
                'training_approved': False,
            } for x in preview['sources']]
        except (KeyError, ValueError, TypeError, OSError) as error:
            entries = {'read_error': str(error)}
    else:
        entries = {'report_exists': False}
    print(json.dumps({'run001_source_preview': entries,
                      'exit_exists': (ROOT / (preview_name + '.exit')).is_file()},
                     ensure_ascii=False))
    tokenizer_job = 'posta-multi-run001-tokenizer-intake-v1'
    tokenizer_file = ROOT / (tokenizer_job + '.json')
    if tokenizer_file.is_file():
        try:
            tokenizer = json.loads(tokenizer_file.read_text(encoding='utf-8'))
            if tokenizer.get('format') != 'posta-run001-tokenizer-intake-v1':
                raise ValueError('unexpected tokenizer intake schema')
            summary = {key: tokenizer[key] for key in (
                'languages', 'manifest_sha256', 'selected_train_counts',
                'selected_validation_counts', 'heldout', 'approval_issues',
                'ready_for_manual_tokenizer_training')}
        except (OSError, ValueError, KeyError, TypeError) as error:
            summary = {'read_error': str(error)}
    else:
        summary = {'report_exists': False}
    print(json.dumps({'run001_tokenizer_intake': summary,
                      'exit_exists': (ROOT / (tokenizer_job + '.exit')).is_file()},
                     ensure_ascii=False))
    content_job = 'posta-multi-run001-content-samples-v1'
    content_root = ROOT / content_job
    content_manifest = content_root / 'manifest.json'
    if content_manifest.is_file():
        try:
            content = json.loads(content_manifest.read_text(encoding='utf-8'))
            if content.get('format') != 'posta-235m-content-source-quarantine-v1':
                raise ValueError('unexpected content source manifest format')
            summary = {
                'status': content['status'],
                'training_approved': content['training_approved'],
                'sources': [
                    {'id': source['id'], 'counts': source['counts'],
                     'observed_head_revision': source['observed_head_revision']}
                    for source in content['sources']
                ],
                'provenance_limitation': 'Dataset Server rows are not revision-pinned',
            }
        except (OSError, KeyError, TypeError, ValueError) as error:
            summary = {'read_error': str(error)}
    else:
        summary = {'report_exists': False}
    print(json.dumps({
        'run001_content_quarantine': summary,
        'exit_exists': (ROOT / (content_job + '.exit')).is_file(),
    }, ensure_ascii=False))
    print('Progress reflects saved files, not live GPU activity or production-quality retrieval.')


if __name__ == '__main__':
    main()
