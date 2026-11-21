"""One tiny CPU training step per data task; not a 235M trainer."""
import argparse
import io
import json
from dataclasses import replace

import torch
from PIL import Image
from corpus import Corpus, KINDS
from corpus_smoke import ffmpeg, log_mel, rgb
from loss import retrieval_loss
from model import PostaMulti
from synthetic_train import tiny_config
from tokenizer import MASK, encode


def tokens(text, length):
    ids = torch.tensor([encode(text, length)])
    return ids, torch.ones_like(ids, dtype=torch.bool)


def vector(model, kind, row, corpus, config):
    if kind == 'retrieval':
        ids, mask = tokens(row['positive'], config.text_length)
        return model.encode_text(ids, mask)
    path = corpus.media_path(row)
    if kind in ('image_text', 'meme_text', 'ocr_text'):
        with Image.open(path) as image:
            return model.encode_image(rgb(image, config.image_size))
    if kind == 'video_text':
        data = ffmpeg('-i', path, '-frames:v', '1', '-f', 'image2pipe', '-vcodec', 'png', 'pipe:1')
        with Image.open(io.BytesIO(data)) as image:
            return model.encode_video(rgb(image, config.image_size).unsqueeze(1))
    if kind == 'speech_text':
        return model.encode_audio(log_mel(path))
    raise ValueError('unsupported task')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    args = parser.parse_args()
    torch.set_num_threads(2)
    torch.manual_seed(42)
    corpus = Corpus(args.dataset)
    config = replace(tiny_config(), vocab_size=260)
    model = PostaMulti(config, dropout=0)
    optimizer = torch.optim.SGD(model.parameters(), lr=0.001)
    results = {}
    for kind in KINDS:
        iterator = corpus.rows(kind)
        rows = []
        for row in iterator:
            # Do not use obviously identical strings or media as negatives.
            if not rows or (row['text'] != rows[0]['text'] and
                (not row.get('media_sha256') or row.get('media_sha256') != rows[0].get('media_sha256')) and
                (not row.get('group_id') or row.get('group_id') != rows[0].get('group_id'))):
                rows.append(row)
            if len(rows) == 2:
                break
        if len(rows) != 2:
            raise ValueError(f'insufficient distinct records: {kind}')
        optimizer.zero_grad(set_to_none=True)
        if kind == 'text':
            ids, mask = tokens(rows[0]['text'], config.text_length)
            selected = torch.zeros_like(ids, dtype=torch.bool)
            selected[0, 1] = True
            target = ids[selected].clone()
            ids[selected] = MASK
            loss = torch.nn.functional.cross_entropy(model.masked_token_logits(ids, mask, selected), target)
        else:
            queries, candidates = [], []
            for row in rows:
                ids, mask = tokens(row['text'], config.text_length)
                queries.append(model.encode_text(ids, mask))
                candidates.append(vector(model, kind, row, corpus, config))
            loss = retrieval_loss(torch.cat(queries), torch.cat(candidates), torch.eye(2, dtype=torch.bool))
        if not torch.isfinite(loss):
            raise ValueError(f'non-finite loss: {kind}')
        loss.backward()
        if any(p.grad is not None and not torch.isfinite(p.grad).all() for p in model.parameters()):
            raise ValueError(f'non-finite gradient: {kind}')
        optimizer.step()
        results[kind] = {'records': 1 if kind == 'text' else 2, 'finite': True}
    print(json.dumps({'manifest_sha256': corpus.manifest_hash, 'steps': results,
                      'model': 'tiny random CPU', 'checkpoint_saved': False}))


if __name__ == '__main__':
    main()
