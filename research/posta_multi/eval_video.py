"""Small local held-out video-caption comparison, using up to two frames."""
import argparse
import hashlib
import json
from pathlib import Path
import numpy as np
import torch
from tokenizers import Tokenizer
from corpus import Corpus
from corpus_smoke import ffmpeg
from model import PostaMulti
from spec import Config


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--dataset', required=True)
    p.add_argument('--checkpoint', required=True)
    p.add_argument('--tokenizer', required=True)
    p.add_argument('--samples', type=int, default=8)
    args = p.parse_args()
    if args.samples < 2 or not torch.cuda.is_available():
        raise ValueError('at least two examples and CUDA required')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(0.45)
    corpus = Corpus(args.dataset)
    rows, seen_text, seen_media = [], set(), set()
    for row in corpus.rows('video_text', 'validation'):
        sha = (row.get('media') or {}).get('sha256')
        if not sha or not row['text'].strip() or row['text'] in seen_text or sha in seen_media:
            continue
        rows.append(row); seen_text.add(row['text']); seen_media.add(sha)
        if len(rows) == args.samples:
            break
    if len(rows) != args.samples:
        raise ValueError('insufficient validation data')
    digest = hashlib.sha256(json.dumps([(r['text'], r['media']['sha256']) for r in rows],
                                      ensure_ascii=False, separators=(',', ':')).encode()).hexdigest()
    state = torch.load(args.checkpoint, map_location='cpu', weights_only=True)
    settings = state['settings']
    if hashlib.sha256(Path(args.tokenizer).read_bytes()).hexdigest() != settings['tokenizer']:
        raise ValueError('tokenizer mismatch')
    if corpus.manifest_hash != settings['manifest_sha256']:
        raise ValueError('dataset mismatch')
    config, step = Config(**settings['config']), state['step']
    weights = state['model']; del state
    model = PostaMulti(config).half().cuda(); model.load_state_dict(weights); del weights
    model.eval()
    tokenizer = Tokenizer.from_file(args.tokenizer)
    tokenizer.enable_truncation(max_length=128)
    queries, videos = [], []
    with torch.no_grad():
        for row in rows:
            ids = torch.tensor([tokenizer.encode(row['text']).ids], device='cuda')
            queries.append(model.encode_text(ids, torch.ones_like(ids, dtype=torch.bool)).float())
            size = config.image_size
            data = ffmpeg('-i', corpus.media_path(row), '-vf', f'fps=1,scale={size}:{size}',
                          '-frames:v', '2', '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1')
            frame_size = size*size*3
            if len(data) < frame_size or len(data) % frame_size:
                raise ValueError('failed to decode video')
            array = np.frombuffer(data, dtype=np.uint8).copy().reshape(-1, size, size, 3)
            frames = torch.from_numpy(array).permute(0, 3, 1, 2).unsqueeze(0).half().cuda() / 255
            videos.append(model.encode_video(frames).float())
        scores = torch.cat(queries) @ torch.cat(videos).T
        wins = sum(float(scores[i,i]) > float(scores[i,(i+1)%len(rows)]) for i in range(len(rows)))
    print(json.dumps({'samples':len(rows), 'wins':wins, 'pair_accuracy':wins/len(rows),
                      'checkpoint_step':step, 'validation_sha256':digest,
                      'note':'local video-caption comparison with up to two frames'}))


if __name__ == '__main__':
    main()
