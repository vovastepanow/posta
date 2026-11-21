"""Small CPU-only training smoke test. Never loads a corpus or the 235M model."""
import argparse
import os
import tempfile
from dataclasses import asdict
from pathlib import Path

import torch

if __package__:
    from .loss import retrieval_loss
    from .model import PostaMulti
    from .spec import Config
else:
    from loss import retrieval_loss
    from model import PostaMulti
    from spec import Config


def tiny_config():
    return Config(vocab_size=128, text_width=32, text_layers=1, text_heads=4,
                  text_ff=64, text_length=16, image_width=32, image_layers=1,
                  image_heads=4, image_ff=64, image_size=32, image_patch=8,
                  embed_dim=16, temporal_width=16, temporal_layers=1,
                  temporal_heads=4, temporal_ff=32, temporal_length=32,
                  fusion_layers=1, mel_bins=16, audio_patch_mels=8, audio_patch_frames=4)


def run(steps, checkpoint, resume=False, seed=42):
    """Run until total step count; resume restores model, optimizer and RNG."""
    if steps < 0:
        raise ValueError('steps must be non-negative')
    torch.manual_seed(seed)
    torch.set_num_threads(2)
    config = tiny_config()
    model = PostaMulti(config, dropout=0.1)
    optimizer = torch.optim.AdamW(model.parameters(), lr=0.001)
    step = 0
    path = Path(checkpoint)
    if resume:
        state = torch.load(path, map_location='cpu', weights_only=True)
        if state['format'] != 'posta-synthetic-v1' or state['config'] != asdict(config):
            raise ValueError('incompatible synthetic checkpoint')
        model.load_state_dict(state['model'])
        optimizer.load_state_dict(state['optimizer'])
        torch.set_rng_state(state['torch_rng'])
        step = state['step']
        if steps < step:
            raise ValueError('target steps precede checkpoint')
    elif path.exists():
        raise FileExistsError('checkpoint already exists; use --resume or choose another path')
    model.train()
    for step in range(step + 1, steps + 1):
        tokens = torch.randint(1, config.vocab_size, (4, 8))
        mask = torch.ones_like(tokens, dtype=torch.bool)
        query = model.encode_text(tokens, mask)
        candidates = model.encode_text(tokens, mask)
        loss = retrieval_loss(query, candidates, torch.eye(4, dtype=torch.bool))
        if not torch.isfinite(loss):
            raise RuntimeError('non-finite loss; checkpoint was not updated')
        optimizer.zero_grad(set_to_none=True)
        loss.backward()
        optimizer.step()
    state = {'format': 'posta-synthetic-v1', 'config': asdict(config), 'step': steps,
             'model': model.state_dict(), 'optimizer': optimizer.state_dict(),
             'torch_rng': torch.get_rng_state(), 'data': 'synthetic random token IDs'}
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.posta-', suffix='.tmp', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as stream:
            torch.save(state, stream)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    return state


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--steps', type=int, required=True, help='total training steps')
    parser.add_argument('--checkpoint', required=True)
    parser.add_argument('--resume', action='store_true')
    args = parser.parse_args()
    state = run(args.steps, args.checkpoint, args.resume)
    print(f"Synthetic CPU checkpoint saved at step {state['step']}: {args.checkpoint}")


if __name__ == '__main__':
    main()
