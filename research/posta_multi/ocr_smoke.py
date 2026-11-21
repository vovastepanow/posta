"""Bounded CPU integration check on the pinned NVIDIA synthetic OCR train set.

Only tests the text input and masked-token loss on a tiny model. No checkpoint.
"""
import argparse
import hashlib
import json
from pathlib import Path

import torch
import torch.nn.functional as F
from model import PostaMulti
from synthetic_train import tiny_config
from tokenizer import CLS, MASK, encode

REVISION = '69696a1cc543ef3a0f8e9892a89c17293e915263'
SOURCES = {'nvidia-ocr-en', 'nvidia-ocr-ru'}


def load_examples(directory, limit=4):
    root = Path(directory)
    manifest = json.loads((root / 'manifest.json').read_text(encoding='utf-8'))
    entries = [x for x in manifest['files'] if x.get('path') == 'ocr_text-train.jsonl']
    if len(entries) != 1:
        raise ValueError('missing or ambiguous OCR train file')
    path = root / 'ocr_text-train.jsonl'
    with path.open('rb') as f:
        if hashlib.file_digest(f, 'sha256').hexdigest() != entries[0]['sha256']:
            raise ValueError('OCR train hash mismatch')
        f.seek(0)
        rows = []
        for line in f:
            row = json.loads(line)
            if (row.get('dataset_id') not in SOURCES or row.get('dataset_revision') != REVISION
                or row.get('split') != 'train' or row.get('kind') != 'ocr_text'):
                raise ValueError('unexpected OCR source, revision, split or kind')
            text = row.get('ocr')
            if isinstance(text, str) and text.strip():
                rows.append(text)
                if len(rows) >= limit:
                    return rows
    raise ValueError('insufficient OCR examples')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    args = parser.parse_args()
    texts = load_examples(args.dataset)
    torch.set_num_threads(2)
    torch.manual_seed(42)
    config = tiny_config()
    # The temporary byte tokenizer uses 260 IDs, unlike the earlier synthetic model.
    from dataclasses import replace
    config = replace(config, vocab_size=260)
    model = PostaMulti(config, dropout=0)
    optim = torch.optim.SGD(model.parameters(), lr=0.01)
    sequences = [encode(text, config.text_length) for text in texts]
    width = max(map(len, sequences))
    tokens = torch.zeros((len(sequences), width), dtype=torch.long)
    attention = torch.zeros_like(tokens, dtype=torch.bool)
    selected = torch.zeros_like(tokens, dtype=torch.bool)
    for i, seq in enumerate(sequences):
        tokens[i, :len(seq)] = torch.tensor(seq)
        attention[i, :len(seq)] = True
        selected[i, 1] = True
    assert (tokens[:, 0] == CLS).all()
    targets = tokens[selected].clone()
    tokens[selected] = MASK
    loss = F.cross_entropy(model.masked_token_logits(tokens, attention, selected), targets)
    loss.backward()
    if not torch.isfinite(loss) or not torch.isfinite(model.text_tokens.weight.grad).all():
        raise RuntimeError('non-finite loss or gradients')
    optim.step()
    print(json.dumps({'samples': len(texts), 'loss_finite': True, 'step': 1,
                      'device': 'cpu', 'configuration': 'tiny', 'checkpoint_saved': False,
                      'source_revision': REVISION}))


if __name__ == '__main__':
    main()
