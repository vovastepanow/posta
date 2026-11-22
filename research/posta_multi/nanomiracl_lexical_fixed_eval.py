"""One fixed lexical comparison on the frozen NanoMIRACL evaluation set.

The word/character fusion weight is fixed from internal validation before this
evaluation. It is not fitted on NanoMIRACL. This measures lexical retrieval,
not the Posta neural model or multimodal retrieval.
"""
import argparse
import hashlib
import json
from pathlib import Path
import random

from compare_checkpoints import atomic_report
from frozen_nanomiracl_eval import load_frozen, score_ranking, summarize_scores
from hybrid_retrieval import fuse_scores
from nanomiracl_benchmark import BM25Index, LANGUAGES
from run001_lexical_validation import CharacterBM25


FIXED_CHAR_WEIGHT = 0.5
RANK_CONSTANT = 60


def paired_intervals(baseline_rows, fusion_rows, *, draws=2000, seed=1001):
    if len(baseline_rows) != len(fusion_rows) or len(baseline_rows) < 2 or draws < 100:
        raise ValueError('paired comparison requires aligned samples')
    rng = random.Random(seed)
    n = len(baseline_rows)
    differences = {key: [new[key] - old[key] for old, new in zip(baseline_rows, fusion_rows)]
                   for key in ('hit_5', 'mrr')}
    resampled = {key: [] for key in differences}
    for _ in range(draws):
        indices = [rng.randrange(n) for _ in range(n)]
        for key, values in differences.items():
            resampled[key].append(sum(values[i] for i in indices) / n)
    result = {}
    for key, values in differences.items():
        samples = sorted(resampled[key])
        result[key] = {'delta': round(sum(values) / n, 6),
                       'bootstrap_95_low': round(samples[int(.025 * (draws-1))], 6),
                       'bootstrap_95_high': round(samples[int(.975 * (draws-1))], 6)}
    return result


def evaluate(groups, baseline):
    results = {}
    for language in LANGUAGES:
        data = groups[language]
        docs = data['corpus']
        word = BM25Index(docs)
        chars = CharacterBM25([docs[doc_id] for doc_id in word.ids])
        original, combined = [], []
        for query_id in sorted(data['queries']):
            query = data['queries'][query_id]
            relevant = set(data['qrels'][query_id])
            lexical = word.scores(query)
            combined_scores = fuse_scores(lexical, chars.scores(query),
                semantic_weight=FIXED_CHAR_WEIGHT, rank_constant=RANK_CONSTANT)
            original.append(score_ranking(word.ids, lexical, relevant))
            combined.append(score_ranking(word.ids, combined_scores, relevant))
        word_result, combined_result = summarize_scores(original), summarize_scores(combined)
        for old_key, new_key in (('recall_1', 'hit_1'), ('recall_5', 'hit_5'),
                                 ('recall_10', 'hit_10'), ('mrr', 'mrr'), ('ndcg_10', 'ndcg_10')):
            if abs(baseline['languages'][language][old_key] - word_result[new_key]) > 1e-6:
                raise ValueError('word BM25 does not reproduce frozen baseline')
        results[language] = {'word_bm25': word_result, 'fixed_word_char_fusion': combined_result,
                             'paired_bootstrap': paired_intervals(original, combined)}
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--frozen', type=Path, required=True)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--calibration-report', type=Path, required=True)
    parser.add_argument('--overlap-report', type=Path, required=True)
    parser.add_argument('--validation-overlap-report', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError('fixed evaluation report already exists')
    calibration_raw = args.calibration_report.read_bytes()
    calibration = json.loads(calibration_raw)
    if (calibration.get('format') != 'posta-run001-lexical-internal-validation-v1' or
            calibration.get('calibration_samples') != 64 or
            calibration.get('selected_char_weight') != FIXED_CHAR_WEIGHT):
        raise ValueError('fixed weight does not match prior internal calibration')
    groups, baseline, frozen_sha = load_frozen(args.frozen, args.baseline)
    overlap = json.loads(args.overlap_report.read_text(encoding='utf-8'))
    matches = overlap.get('unique_benchmark_texts_matched')
    if (overlap.get('format') != 'posta-nanomiracl-exact-train-overlap-v1' or
            overlap.get('manifest_sha256') != calibration.get('manifest_sha256') or
            overlap.get('frozen_file_sha256') != frozen_sha or
            not isinstance(matches, dict) or
            set(matches) != {'ru:queries', 'ru:corpus', 'en:queries', 'en:corpus'} or
            any(type(value) is not int or value != 0 for value in matches.values())):
        raise ValueError('matching zero-overlap audit is required')
    validation_overlap = json.loads(args.validation_overlap_report.read_text(encoding='utf-8'))
    validation_matches = validation_overlap.get('unique_benchmark_texts_matched')
    if (validation_overlap.get('format') != 'posta-nanomiracl-exact-validation-overlap-v1' or
            validation_overlap.get('manifest_sha256') != calibration.get('manifest_sha256') or
            validation_overlap.get('frozen_file_sha256') != frozen_sha or
            not isinstance(validation_matches, dict) or
            set(validation_matches) != {'ru:queries', 'ru:corpus', 'en:queries', 'en:corpus'} or
            any(type(value) is not int or value != 0 for value in validation_matches.values())):
        raise ValueError('matching zero-overlap validation audit is required')
    report = {'format': 'posta-nanomiracl-fixed-lexical-evaluation-v1',
              'evaluation_only': True, 'frozen_file_sha256': frozen_sha,
              'source_content_sha256': baseline['source_content_sha256'],
              'calibration_report_sha256': hashlib.sha256(calibration_raw).hexdigest(),
              'calibration_manifest_sha256': calibration['manifest_sha256'],
              'fixed_char_weight': FIXED_CHAR_WEIGHT, 'rank_constant': RANK_CONSTANT,
              'languages': evaluate(groups, baseline),
              'limitations': ['lexical methods only; no neural or multimodal model evaluated',
                              'NanoMIRACL is a small MIRACL-derived subset, not Posta posts',
                              'unjudged documents may be relevant',
                              'training overlap audit detects exact matches only',
                              'paired bootstrap resamples queries and assumes they are exchangeable']}
    atomic_report(args.output, report)
    print(json.dumps(report, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
