"""Bounded masked-token evaluation on local validation data, never train split."""
import argparse
import hashlib
import json
from collections import defaultdict

import torch
import torch.nn.functional as F
from tokenizers import Tokenizer
from corpus import Corpus
from model import PostaMulti
from spec import Config


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--per-group', type=int, default=2)
    args = p.parse_args()
    if args.per_group < 1:
        raise ValueError('per-group must be positive')
    torch.set_num_threads(2)
    state = torch.load(args.checkpoint, map_location='cpu', weights_only=True)
    if state.get('format') != 'posta-text-v1':
        raise ValueError('unexpected checkpoint')
    tokenizer_hash = hashlib.sha256(open(args.tokenizer, 'rb').read()).hexdigest()
    if state['settings']['tokenizer'] != tokenizer_hash:
        raise ValueError('tokenizer hash differs from checkpoint')
    config = Config(**state['settings']['config'])
    corpus = Corpus(args.dataset)
    if corpus.manifest_hash != state['settings']['manifest_sha256']:
        raise ValueError('dataset manifest differs from checkpoint')
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=config.text_length)
    step = state['step']
    weights = state['model']
    del state
    device = 'cuda' if torch.cuda.is_available() else 'cpu'
    if device == 'cuda':
        torch.cuda.set_per_process_memory_fraction(0.45)
        model = PostaMulti(config).half().cuda()
    else:
        model = PostaMulti(config)
    model.load_state_dict(weights)
    del weights
    model.eval()
    metrics = defaultdict(lambda: {'records': 0, 'targets': 0, 'loss_sum': 0.0, 'correct': 0})
    with torch.no_grad():
        for kind in ('text', 'ocr_text'):
            for row in corpus.rows(kind, 'validation'):
                language = row.get('language')
                if language not in ('ru', 'en'):
                    continue
                key = f'{kind}:{language}'
                item = metrics[key]
                if item['records'] >= args.per_group:
                    continue
                ids = torch.tensor([tokenizer.encode(row['text']).ids], dtype=torch.long, device=device)
                if ids.shape[1] < 3:
                    continue
                count = min(64, max(1, round(0.15 * (ids.shape[1] - 2))))
                indices = torch.linspace(1, ids.shape[1] - 2, count, device=device).long()
                selected = torch.zeros_like(ids, dtype=torch.bool)
                selected[0, indices] = True
                target = ids[selected].clone()
                ids[selected] = 3
                logits = model.masked_token_logits(ids, torch.ones_like(ids, dtype=torch.bool), selected).float()
                item['loss_sum'] += float(F.cross_entropy(logits, target, reduction='sum'))
                item['correct'] += int((logits.argmax(-1) == target).sum())
                item['targets'] += target.numel()
                item['records'] += 1
                if all(metrics[f'{kind}:{lang}']['records'] >= args.per_group for lang in ('ru', 'en')):
                    break
    output = {}
    for key, value in metrics.items():
        if value['targets']:
            output[key] = {'records': value['records'], 'masked_tokens': value['targets'],
                           'mean_loss': round(value['loss_sum']/value['targets'], 4),
                           'token_accuracy': round(value['correct']/value['targets'], 4)}
    print(json.dumps({'checkpoint_step': step, 'device': device, 'local_validation': output,
                      'note': 'masked-token validation, not retrieval or multimodal quality'}, ensure_ascii=False))


if __name__ == '__main__':
    main()
