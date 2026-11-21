"""Posta Multi 235M Run 001 multilingual tokenizer intake and candidate training.

Manual train-only corpus fitting; --audit-only never trains or creates files.
A manifest's approval flags are necessary but are NOT independent evidence
of permissions, privacy, data quality or benchmark isolation.
Never fit on validation, test, NanoMIRACL or external preview JSONL.
"""
import argparse
from collections import Counter, defaultdict
import hashlib
import json
import os
from pathlib import Path
import statistics
import unicodedata

from corpus import Corpus
from run001_data import (KINDS, approval_issues, select_records, text_fields)
from run001_research_policy import require_local_research_quarantine, PENDING_FLAGS

SPECIALS = ('[PAD]', '[CLS]', '[SEP]', '[MASK]')
POLICY = 'posta-run001-tokenizer-balanced-kind-language-v1'
MAX_CHARS = 3072


def fingerprint(text):
    normalized = ' '.join(unicodedata.normalize('NFKC', text).casefold().split())
    return hashlib.sha256(normalized.encode('utf-8')).digest() if normalized else None


def languages_arg(value):
    languages = tuple(part.strip().lower() for part in value.split(','))
    if (not languages or any(not lang or not lang.isalpha() or len(lang) > 12
                              for lang in languages) or len(set(languages)) != len(languages)):
        raise ValueError('languages must be distinct, comma-separated language tags')
    return languages


def balanced_texts(selected, languages, per_language):
    """Deterministic language-first quotas with round-robin modality coverage.

    Input is already bounded by select_records' hash heaps per (kind, language).
    Cross-kind/cross-language normalized repeats do not get extra votes.
    """
    if type(per_language) is not int or per_language < 1:
        raise ValueError('per_language must be positive')
    buckets = {lang: defaultdict(list) for lang in languages}
    for (kind, lang), text in selected:
        if lang in buckets:
            digest = hashlib.sha256(text.encode('utf-8')).digest()
            buckets[lang][kind].append((digest, text))
    ordered = {lang: {kind: sorted(rows) for kind, rows in group.items()}
               for lang, group in buckets.items()}
    positions = {lang: {kind: 0 for kind in group} for lang, group in ordered.items()}
    accepted, seen_global = [], set()
    counts = Counter()
    for lang in languages:
        kinds = sorted(ordered[lang])
        while counts[lang] < per_language:
            progress = False
            for kind in kinds:
                entries = ordered[lang][kind]
                pos = positions[lang][kind]
                while pos < len(entries):
                    _, text = entries[pos]
                    pos += 1
                    fp = fingerprint(text)
                    if fp is not None and fp not in seen_global:
                        seen_global.add(fp)
                        accepted.append((lang, kind, text))
                        counts[lang] += 1
                        progress = True
                        break
                positions[lang][kind] = pos
                if counts[lang] == per_language:
                    break
            if not progress:
                break
    return accepted, {lang: counts[lang] for lang in languages}


def selection_sha(items):
    digest = hashlib.sha256(POLICY.encode('utf-8'))
    for lang, kind, text in items:
        raw = json.dumps([lang, kind, text], ensure_ascii=False,
                         separators=(',', ':')).encode('utf-8')
        digest.update(len(raw).to_bytes(8, 'big'))
        digest.update(raw)
    return digest.hexdigest()


def heldout_collisions(corpus, train):
    """Scan ALL validation/test row roles, compare to selected train text.

    Test content is used only to calculate fingerprints, never to fit or
    choose tokenizer parameters. Report counts, never raw text or hashes.
    """
    train_fingerprints = {fp for _, _, text in train if (fp := fingerprint(text))}
    results = {}
    for split in ('validation', 'test'):
        matched = set()
        checked = 0
        for kind in KINDS:
            for row in corpus.rows(kind, split):
                for text in text_fields(kind, row):
                    fp = fingerprint(text[:MAX_CHARS])
                    if fp:
                        checked += 1
                        if fp in train_fingerprints:
                            matched.add(fp)
        results[split] = {'text_fields_checked': checked,
                          'distinct_selected_train_overlaps': len(matched)}
    return results


def inspect(corpus, languages, *, per_group, per_language, min_documents,
            validation_per_group, min_validation):
    selected, group_summary = select_records(
        corpus, split='train', max_per_group=per_group, max_chars=MAX_CHARS)
    training, lang_counts = balanced_texts(selected, languages, per_language)
    validation, val_group_summary = select_records(
        corpus, split='validation', max_per_group=validation_per_group,
        max_chars=MAX_CHARS)
    validation_counts = {lang: sum(group[1] == lang for group, _ in validation)
                         for lang in languages}
    collisions = heldout_collisions(corpus, training)
    issues = approval_issues(corpus.manifest)
    if any(lang_counts[lang] < min_documents for lang in languages):
        issues.append('insufficient_train_documents_per_required_language')
    if any(validation_counts[lang] < min_validation for lang in languages):
        issues.append('insufficient_validation_documents_per_required_language')
    if any(item['distinct_selected_train_overlaps'] for item in collisions.values()):
        issues.append('train_tokenizer_input_overlaps_heldout_text')
    report = {
        'format': 'posta-run001-tokenizer-intake-v1',
        'policy': POLICY,
        'manifest_sha256': corpus.manifest_hash,
        'languages': list(languages),
        'requested_train_per_language': per_language,
        'minimum_train_per_language': min_documents,
        'minimum_validation_per_language': min_validation,
        'selected_train_counts': lang_counts,
        'selected_validation_counts': validation_counts,
        'training_selection_sha256': selection_sha(training),
        'train_kind_language_groups': group_summary,
        'validation_kind_language_groups': val_group_summary,
        'heldout': collisions,
        'approval_issues': issues,
        'ready_for_manual_tokenizer_training': not issues,
        'source_review_is_documentary_only': True,
        'limitations': [
            'Dataset source rights/privacy flags require independent review',
            'Tokenizer input is a bounded hash selection, not all dataset rows',
            'Overlap check is exact normalized text for selected training input',
            'Near-duplicates, translations, PII and other benchmark contamination are not ruled out',
            'No evaluation/test text is used to fit or tune the tokenizer',
            'Language tags are taken from corpus metadata and are not independently detected',
        ],
    }
    return training, [((kind, lang), text) for (kind, lang), text in validation
                      if lang in languages], report


def percentile(values, pct):
    if not values:
        raise ValueError('empty token-length sample')
    ordered = sorted(values)
    return ordered[min(len(ordered)-1, (len(ordered)-1)*pct // 100)]


def validation_metrics(tokenizer, heldout, languages):
    stats = {lang: {'lengths': [], 'bytes': 0, 'tokens': 0}
             for lang in languages}
    for (_, lang), text in heldout:
        ids = tokenizer.encode(text, add_special_tokens=False).ids
        if not ids or tokenizer.decode(ids, skip_special_tokens=False) != text:
            raise ValueError('validation Unicode round-trip failed for ' + lang)
        values = stats[lang]
        values['lengths'].append(len(ids))
        values['bytes'] += len(text.encode('utf-8'))
        values['tokens'] += len(ids)
    results = {}
    for lang, values in stats.items():
        lengths = values['lengths']
        if not lengths:
            raise ValueError('no held-out validation samples for ' + lang)
        results[lang] = {
            'documents': len(lengths),
            'tokens_p50': percentile(lengths, 50),
            'tokens_p90': percentile(lengths, 90),
            'tokens_p99': percentile(lengths, 99),
            'fraction_exceeding_96_tokens': round(sum(n > 94 for n in lengths) / len(lengths), 5),
            'fraction_exceeding_512_tokens': round(sum(n > 510 for n in lengths) / len(lengths), 5),
            'utf8_bytes_per_token': round(values['bytes'] / values['tokens'], 4),
            'unicode_round_trip_passed': True,
        }
    return results


def train(args):
    if (args.per_group < 1 or args.per_language < 1 or
            args.min_documents < 1 or args.validation_per_group < 1 or
            args.min_validation < 1 or not 260 <= args.vocab_size <= 65536):
        raise ValueError('invalid tokenizer sampling or vocabulary budget')
    languages = languages_arg(args.languages)
    corpus = Corpus(args.dataset)
    examples, validation, report = inspect(
        corpus, languages, per_group=args.per_group,
        per_language=args.per_language, min_documents=args.min_documents,
        validation_per_group=args.validation_per_group,
        min_validation=args.min_validation)
    if args.audit_only:
        print(json.dumps(report, ensure_ascii=False, sort_keys=True))
        return report
    local_research = getattr(args, 'local_research_quarantine', False)
    if local_research:
        require_local_research_quarantine(corpus)
        if set(report['approval_issues']) != PENDING_FLAGS:
            raise ValueError('research tokenizer still blocked by quality or overlap: ' +
                             ', '.join(report['approval_issues']))
    elif not report['ready_for_manual_tokenizer_training']:
        raise ValueError('tokenizer training blocked: ' + ', '.join(report['approval_issues']))
    if args.output is None:
        raise ValueError('--output required unless --audit-only')
    destination = Path(args.output)
    if destination.exists():
        raise FileExistsError('refuse to overwrite tokenizer output')
    # Imported ONLY after the review and split checks pass.
    import tokenizers as library
    from tokenizers import Tokenizer, decoders, models, pre_tokenizers, processors, trainers
    tokenizer = Tokenizer(models.BPE(unk_token=None))
    tokenizer.pre_tokenizer = pre_tokenizers.ByteLevel(add_prefix_space=False)
    tokenizer.decoder = decoders.ByteLevel()
    trainer = trainers.BpeTrainer(
        vocab_size=args.vocab_size, special_tokens=list(SPECIALS),
        initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False)
    tokenizer.train_from_iterator((text for _, _, text in examples),
                                  trainer=trainer, length=len(examples))
    tokenizer.post_processor = processors.TemplateProcessing(
        single='[CLS] $A [SEP]', special_tokens=[('[CLS]', 1), ('[SEP]', 2)])
    if any(tokenizer.token_to_id(token) != i for i, token in enumerate(SPECIALS)):
        raise RuntimeError('unexpected tokenizer special IDs')
    metrics = validation_metrics(tokenizer, validation, languages)
    for text in ('Привет, мир! 🌍', 'Hello! [CLS] & café', '你好世界', 'مرحبا بالعالم'):
        ids = tokenizer.encode(text, add_special_tokens=False).ids
        if tokenizer.decode(ids, skip_special_tokens=False) != text:
            raise ValueError('multilingual Unicode smoke round-trip failed')
    size = tokenizer.get_vocab_size()
    if not 260 <= size <= args.vocab_size:
        raise ValueError('trained vocabulary outside 235M budget')
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.mkdir(mode=0o700, exist_ok=False)
    try:
        tokenizer_path = destination / 'tokenizer.json'
        tokenizer.save(str(tokenizer_path))
        metadata = {
            **report,
            'format': 'posta-run001-tokenizer-candidate-v1',
            'status': ('quarantined_local_research_only_not_releaseable'
                       if local_research else 'candidate_needs_independent_language_and_domain_review'),
            'quarantined_research_only': local_research,
            'private_local_only': local_research,
            'production_promotion_allowed': False,
            'model_vocabulary_budget': 65536,
            'requested_vocab_size': args.vocab_size,
            'actual_vocab_size': size,
            'special_token_ids': dict(zip(SPECIALS, range(4))),
            'tokenizers_version': library.__version__,
            'tokenizer_sha256': hashlib.sha256(tokenizer_path.read_bytes()).hexdigest(),
            'validation_metrics': metrics,
            'train_samples_by_modality': dict(sorted(Counter(
                kind for _, kind, _ in examples).items())),
            'training_examples': len(examples),
        }
        (destination / 'metadata.json').write_text(
            json.dumps(metadata, ensure_ascii=False, indent=2, sort_keys=True) + '\n',
            encoding='utf-8')
        os.chmod(tokenizer_path, 0o600)
        os.chmod(destination / 'metadata.json', 0o600)
    except BaseException:
        for path in destination.iterdir():
            path.unlink()
        destination.rmdir()
        raise
    print(json.dumps({k: metadata[k] for k in (
        'status', 'manifest_sha256', 'training_selection_sha256',
        'tokenizer_sha256', 'actual_vocab_size', 'training_examples',
        'validation_metrics')}, ensure_ascii=False))
    return metadata


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--audit-only', action='store_true',
                        help='inspect quarantine without fitting vocabulary or writing files')
    parser.add_argument('--local-research-quarantine', action='store_true',
                        help='explicit private nonproduction training experiment on existing screened quarantined data')
    parser.add_argument('--languages', default='ru,en')
    parser.add_argument('--per-group', type=int, default=2500)
    parser.add_argument('--per-language', type=int, default=6000)
    parser.add_argument('--min-documents', type=int, default=3000)
    parser.add_argument('--validation-per-group', type=int, default=128)
    parser.add_argument('--min-validation', type=int, default=32)
    parser.add_argument('--vocab-size', type=int, default=65536)
    train(parser.parse_args())


if __name__ == '__main__':
    main()
