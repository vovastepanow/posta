"""Experimental FP32 CPU-master SGD for FP16 accelerator parameters.

One CPU FP32 master per parameter; no momentum or optimizer state. This trades
speed for GPU memory. It is not Adam and makes no quality guarantees.
"""
import math
import torch


class CPUMasterSGD:
    def __init__(self, params, lr=1e-3, loss_scale=128.0):
        self.params = list(params)
        if not self.params or not math.isfinite(lr) or lr <= 0 or not math.isfinite(loss_scale) or loss_scale <= 0:
            raise ValueError('invalid parameters, learning rate or loss scale')
        if any(p.dtype != torch.float16 for p in self.params):
            raise ValueError('all parameters must be FP16')
        self.lr, self.loss_scale = lr, loss_scale
        self.masters = [p.detach().to(device='cpu', dtype=torch.float32).clone() for p in self.params]
        self.steps = 0

    def zero_grad(self):
        for p in self.params:
            p.grad = None

    def backward(self, loss):
        (loss * self.loss_scale).backward()

    @torch.no_grad()
    def step(self):
        # Check all gradients before touching any weights, so overflow skips the
        # complete update. Detect actual infinity after scaled FP16 backward.
        if any(p.grad is not None and not torch.isfinite(p.grad).all().item() for p in self.params):
            return False
        for p, master in zip(self.params, self.masters):
            if p.grad is not None:
                grad = p.grad.detach().to(device='cpu', dtype=torch.float32)
                master.add_(grad, alpha=-self.lr / self.loss_scale)
                p.copy_(master.to(device=p.device, dtype=p.dtype))
        self.steps += 1
        return True

    def state_dict(self):
        return {'version': 1, 'lr': self.lr, 'loss_scale': self.loss_scale,
                'steps': self.steps, 'masters': self.masters}

    @torch.no_grad()
    def load_state_dict(self, state):
        if state.get('version') != 1 or len(state['masters']) != len(self.params):
            raise ValueError('incompatible optimizer checkpoint')
        if any(a.shape != b.shape for a, b in zip(state['masters'], self.params)):
            raise ValueError('optimizer parameter shapes differ')
        lr, scale = state['lr'], state['loss_scale']
        if not math.isfinite(lr) or lr <= 0 or not math.isfinite(scale) or scale <= 0:
            raise ValueError('invalid optimizer checkpoint settings')
        self.lr, self.loss_scale, self.steps = lr, scale, state['steps']
        for p, master, saved in zip(self.params, self.masters, state['masters']):
            master.copy_(saved)
            p.copy_(master.to(device=p.device, dtype=p.dtype))
