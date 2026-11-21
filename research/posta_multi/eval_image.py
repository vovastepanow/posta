"""Small held-out caption vs two-image comparison; not a retrieval benchmark."""
import argparse
import hashlib
import json
from pathlib import Path
import torch
from PIL import Image
from tokenizers import Tokenizer
from corpus import Corpus
from corpus_smoke import rgb
from model import PostaMulti
from spec import Config


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--tokenizer', required=True)
    parser.add_argument('--samples', type=int, default=8)
    args = parser.parse_args()
    if args.samples < 2 or not torch.cuda.is_available():
        raise ValueError('at least two samples and CUDA are required')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    rows, text_seen, media_seen = [], set(), set()
    for row in corpus.rows('image_text', 'validation'):
        text = row['text']
        media_sha = (row.get('media') or {}).get('sha256')
        if not text.strip() or not media_sha or text in text_seen or media_sha in media_seen:
            continue
        rows.append(row)
        text_seen.add(text)
        media_seen.add(media_sha)
        if len(rows) == args.samples:
            break
    if len(rows) != args.samples:
        raise ValueError('not enough distinct validation examples')
    digest = hashlib.sha256(json.dumps([(r['text'], r['media']['sha256']) for r in rows],
                                      ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()
    state = torch.load(args.checkpoint, map_location='cpu', weights_only=True)
    settings = state['settings']
    if hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest() != settings['tokenizer']:
        raise ValueError('tokenizer mismatch')
    if corpus.manifest_hash != settings['manifest_sha256']:
        raise ValueError('dataset mismatch')
    config, step = Config(**settings['config']), state['step']
    weights = state['model']
    del state
    model = PostaMulti(config).half().cuda()
    model.load_state_dict(weights)
    del weights
    model.eval()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=128)
    text_vectors, image_vectors = [], []
    with torch.no_grad():
        for row in rows:
            ids = torch.tensor([tokenizer.encode(row['text']).ids], device='cuda')
            text_vectors.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).float())
            with Image.open(corpus.media_path(row)) as image:
                image_vectors.append(model.encode_image(rgb(image, config.image_size).half().cuda()).float())
        text = torch.cat(text_vectors)
        image = torch.cat(image_vectors)
        scores = text @ image.T
        wins = sum(float(scores[i, i]) > float(scores[i, (i+1) % len(rows)]) for i in range(len(rows)))
    print(json.dumps({'samples': len(rows), 'wins': wins, 'pair_accuracy': wins/len(rows),
                      'checkpoint_step': step, 'validation_sha256': digest,
                      'note': 'local held-out captions against one unrelated image; not top-k retrieval'}))


if __name__ == '__main__':
    main()
