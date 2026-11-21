"""Train a provisional byte-level BPE tokenizer on balanced NVIDIA OCR labels."""
import argparse
import hashlib
import json
from pathlib import Path

from tokenizers import Tokenizer, decoders, models, pre_tokenizers, processors, trainers
from corpus import Corpus
from train_text import REVISION

SPECIALS = ['[PAD]', '[CLS]', '[SEP]', '[MASK]']
SOURCES = {'nvidia-ocr-ru': 'ru', 'nvidia-ocr-en': 'en'}


def train(dataset, output, per_language=500, vocab_size=65536):
    if per_language < 1 or vocab_size < 260:
        raise ValueError('invalid sample count or vocabulary size')
    output = Path(output)
    if output.exists():
        raise FileExistsError('output directory already exists')
    corpus = Corpus(dataset)
    counts = {'ru': 0, 'en': 0}
    data_hash = hashlib.sha256()

    def texts():
        for row in corpus.rows('ocr_text', 'train'):
            language = SOURCES.get(row['dataset_id'])
            if not language or row['dataset_revision'] != REVISION or counts[language] >= per_language:
                continue
            text = row.get('ocr')
            if not isinstance(text, str) or not text.strip():
                continue
            raw = text.encode('utf-8')
            data_hash.update(len(raw).to_bytes(8, 'big'))
            data_hash.update(raw)
            counts[language] += 1
            yield text
            if all(value == per_language for value in counts.values()):
                break
        if any(value != per_language for value in counts.values()):
            raise ValueError(f'insufficient samples for balanced tokenizer: {counts}')

    tokenizer = Tokenizer(models.BPE(unk_token=None))
    tokenizer.pre_tokenizer = pre_tokenizers.ByteLevel(add_prefix_space=False)
    tokenizer.decoder = decoders.ByteLevel()
    trainer = trainers.BpeTrainer(vocab_size=vocab_size, special_tokens=SPECIALS,
                                  initial_alphabet=pre_tokenizers.ByteLevel.alphabet(), show_progress=False)
    tokenizer.train_from_iterator(texts(), trainer=trainer)
    tokenizer.post_processor = processors.TemplateProcessing(
        single='[CLS] $A [SEP]', special_tokens=[('[CLS]', 1), ('[SEP]', 2)])
    if any(tokenizer.token_to_id(token) != i for i, token in enumerate(SPECIALS)):
        raise RuntimeError('unexpected special token IDs')
    output.mkdir(parents=True, exist_ok=False)
    path = output / 'tokenizer.json'
    tokenizer.save(str(path))
    metadata = {'status': 'experimental_ocr_only', 'tokenizers_version': __import__('tokenizers').__version__,
                'requested_vocab_size': vocab_size, 'actual_vocab_size': tokenizer.get_vocab_size(),
                'special_tokens': dict(zip(SPECIALS, range(4))), 'counts': counts,
                'source_revision': REVISION, 'manifest_sha256': corpus.manifest_hash,
                'training_text_sha256': data_hash.hexdigest(),
                'tokenizer_sha256': hashlib.sha256(path.read_bytes()).hexdigest()}
    (output / 'metadata.json').write_text(json.dumps(metadata, indent=2, ensure_ascii=False)+'\n', encoding='utf-8')
    return metadata


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--output', required=True)
    p.add_argument('--per-language', type=int, default=500)
    p.add_argument('--vocab-size', type=int, default=65536)
    args = p.parse_args()
    print(json.dumps(train(args.dataset, args.output, args.per_language, args.vocab_size)))


if __name__ == '__main__':
    main()
