"""One synthetic full-model CPU-master SGD update; no dataset or checkpoint."""
import json
import math
import time

import torch
from cpu_master_sgd import CPUMasterSGD
from model import PostaMulti
from spec import Config


def main():
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA unavailable')
    torch.set_num_threads(2)
    torch.manual_seed(42)
    torch.cuda.set_per_process_memory_fraction(0.45)
    start = time.monotonic()
    model = PostaMulti(Config(), dropout=0).half().cuda()
    optimizer = CPUMasterSGD(model.parameters(), lr=0.001, loss_scale=16)
    ids = torch.randint(4, 260, (1, 32), device='cuda')
    mask = torch.ones_like(ids, dtype=torch.bool)
    selected = torch.zeros_like(mask)
    selected[0, 1] = True
    labels = ids[selected].clone()
    ids[selected] = 3
    text_index = next(i for i, (name, _) in enumerate(model.named_parameters()) if name == 'text_tokens.weight')
    before = optimizer.masters[text_index].clone()
    loss = torch.nn.functional.cross_entropy(model.masked_token_logits(ids, mask, selected).float(), labels)
    if not torch.isfinite(loss):
        raise RuntimeError('non-finite loss')
    optimizer.backward(loss)
    success = optimizer.step()
    changed = not torch.equal(before, optimizer.masters[text_index])
    if not success or not changed:
        raise RuntimeError(f'overflow or no embedding update: success={success}, changed={changed}')
    torch.cuda.synchronize()
    print(json.dumps({'parameters': sum(p.numel() for p in model.parameters()),
                      'device': torch.cuda.get_device_name(), 'seconds': round(time.monotonic()-start, 2),
                      'loss': float(loss), 'update_success': success, 'embedding_changed': changed,
                      'max_allocated_bytes': torch.cuda.max_memory_allocated(),
                      'max_reserved_bytes': torch.cuda.max_memory_reserved(),
                      'optimizer': 'FP32 CPU-master SGD, no momentum', 'sequence_length': 32,
                      'synthetic': True, 'checkpoint_saved': False}))


if __name__ == '__main__':
    main()
