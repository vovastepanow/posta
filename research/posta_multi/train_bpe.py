"""Train and evaluate a balanced experimental byte-level BPE tokenizer."""
import argparse
import hashlib
import json
from collections import Counter
from pathlib import Path

from tokenizers import Tokenizer, decoders, models, pre_tokenizers, processors, trainers

from corpus import Corpus
from tokenizer import encode as encode_bytes

SPECIALS = ['[PAD]', '[CLS]', '[SEP]', '[MASK]']
# Equal total sample counts per language, with captions, OCR and retrieval.
QUOTAS = {('text', 'ru'): 1000, ('text', 'en'): 1000,
          ('ocr_text', 'ru'): 300, ('ocr_text', 'en'): 300,
          ('image_text', 'ru'): 200, ('image_text', 'en'): 200,
          ('retrieval', 'ru'): 200, ('video_text', 'en'): 200}
KINDS = ('text', 'ocr_text', 'image_text', 'retrieval', 'video_text')


def train(dataset, output, quotas=None, vocab_size=65536, max_chars=4096, eval_limit=100):
    quotas = dict(QUOTAS if quotas is None else quotas)
    if vocab_size < 260 or max_chars < 16 or eval_limit < 1 or any(v < 1 for v in quotas.values()):
        raise ValueError('invalid configuration')
    output = Path(output)
    if output.exists():
        raise FileExistsError('output directory already exists')
    corpus = Corpus(dataset)
    counts = Counter()
    source_counts = Counter()
    digest = hashlib.sha256()

    def texts():
        for kind in KINDS:
            wanted = {lang: quota for (k, lang), quota in quotas.items() if k == kind}
            if not wanted:
                continue
            for row in corpus.rows(kind, 'train'):
                lang = row.get('language')
                if lang not in wanted or counts[(kind, lang)] >= wanted[lang]:
                    continue
                text = row['text'][:max_chars]
                if not text.strip():
                    continue
                raw = text.encode('utf-8')
                digest.update(len(raw).to_bytes(8, 'big'))
                digest.update(raw)
                counts[(kind, lang)] += 1
                source_counts[row['dataset_id']] += 1
                yield text
                if all(counts[(kind, lang)] == n for lang, n in wanted.items()):
                    break
            if any(counts[(kind, lang)] != n for lang, n in wanted.items()):
                raise ValueError(f'insufficient {kind} train records: {counts}')

    tokenizer = Tokenizer(models.BPE(unk_token=None))
    tokenizer.pre_tokenizer = pre_tokenizers.ByteLevel(add_prefix_space=False)
    tokenizer.decoder = decoders.ByteLevel()
    tokenizer.train_from_iterator(texts(), trainer=trainers.BpeTrainer(
        vocab_size=vocab_size, special_tokens=SPECIALS,
        initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False))
    tokenizer.post_processor = processors.TemplateProcessing(
        single='[CLS] $A [SEP]', special_tokens=[('[CLS]', 1), ('[SEP]', 2)])
    if any(tokenizer.token_to_id(token) != i for i, token in enumerate(SPECIALS)):
        raise ValueError('unexpected special tokens')
    metrics = {}
    for kind in ('text', 'ocr_text'):
        stats = {lang: {'samples': 0, 'chars': 0, 'bpe_tokens': 0, 'byte_tokens': 0,
                        'over_512_bpe': 0, 'roundtrip_failures': 0} for lang in ('ru', 'en')}
        for row in corpus.rows(kind, 'validation'):
            lang = row.get('language')
            if lang not in stats or stats[lang]['samples'] >= eval_limit:
                continue
            text = row['text'][:max_chars]
            ids = tokenizer.encode(text).ids
            s = stats[lang]
            s['samples'] += 1
            s['chars'] += len(text)
            s['bpe_tokens'] += len(ids)
            s['byte_tokens'] += len(text.encode('utf-8')) + 2
            s['over_512_bpe'] += len(ids) > 512
            s['roundtrip_failures'] += tokenizer.decode(ids) != text
            if all(value['samples'] == eval_limit for value in stats.values()):
                break
        metrics[kind] = stats
    output.mkdir(parents=True, exist_ok=False)
    path = output / 'tokenizer.json'
    tokenizer.save(str(path))
    metadata = {'status': 'experimental_mixed_corpus', 'tokenizers_version': __import__('tokenizers').__version__,
                'requested_vocab_size': vocab_size, 'actual_vocab_size': tokenizer.get_vocab_size(),
                'special_tokens': dict(zip(SPECIALS, range(4))),
                'counts': {f'{kind}:{lang}': n for (kind, lang), n in sorted(counts.items())},
                'sources': dict(source_counts), 'manifest_sha256': corpus.manifest_hash,
                'training_text_sha256': digest.hexdigest(), 'max_chars_per_sample': max_chars,
                'tokenizer_sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'validation': metrics}
    (output/'metadata.json').write_text(json.dumps(metadata, indent=2, ensure_ascii=False)+'\n', encoding='utf-8')
    return metadata


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--output', required=True)
    p.add_argument('--vocab-size', type=int, default=65536,
                   help='Run 001 235M default; use a reviewed 65k tokenizer from adequate multilingual text')
    args = p.parse_args()
    print(json.dumps(train(args.dataset, args.output, vocab_size=args.vocab_size), ensure_ascii=False))


if __name__ == '__main__':
    main()
