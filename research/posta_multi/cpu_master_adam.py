"""FP32 CPU-master AdamW for selected FP16 parameters."""
import math
import torch


class CPUMasterAdamW:
    def __init__(self, params, lr=1e-4, loss_scale=16.0, betas=(0.9, 0.999), eps=1e-8, weight_decay=0.01, max_grad_norm=None):
        self.params = list(params)
        if not self.params or any(p.dtype != torch.float16 for p in self.params):
            raise ValueError('parameters must be nonempty and FP16')
        if not 0 < lr < 1 or not loss_scale > 0 or not 0 < eps or not 0 <= weight_decay:
            raise ValueError('invalid optimizer settings')
        if not all(0 <= b < 1 for b in betas):
            raise ValueError('invalid betas')
        if max_grad_norm is not None and (not isinstance(max_grad_norm, (int, float))
                                           or not math.isfinite(max_grad_norm)
                                           or max_grad_norm <= 0):
            raise ValueError('max_grad_norm must be positive and finite')
        self.lr, self.loss_scale, self.betas, self.eps, self.weight_decay = lr, loss_scale, betas, eps, weight_decay
        self.max_grad_norm = max_grad_norm
        self.last_grad_norm = None
        self.last_grad_multiplier = 1.0
        self.masters = [p.detach().to(device='cpu', dtype=torch.float32).clone() for p in self.params]
        self.moments = [None for _ in self.params]
        self.steps = 0

    def zero_grad(self):
        for p in self.params:
            p.grad = None

    def backward(self, loss):
        (loss * self.loss_scale).backward()

    @torch.no_grad()
    def step(self):
        available = [p for p in self.params if p.grad is not None]
        if not available:
            return False
        if any(not bool(torch.isfinite(p.grad).all()) for p in available):
            return False
        self.last_grad_multiplier = 1.0
        self.last_grad_norm = None
        if self.max_grad_norm is not None:
            # Accumulate squared FP32 norms as device scalars. Do not collect
            # the huge 235M parameter gradients in a second CPU buffer.
            device = available[0].grad.device
            squared = torch.zeros((), device=device, dtype=torch.float32)
            for param in available:
                magnitude = torch.linalg.vector_norm(
                    param.grad.detach(), ord=2, dtype=torch.float32)
                squared = squared + magnitude.square()
            grad_norm = float(squared.sqrt().item()) / self.loss_scale
            if not math.isfinite(grad_norm):
                return False
            self.last_grad_norm = grad_norm
            if grad_norm > self.max_grad_norm:
                self.last_grad_multiplier = self.max_grad_norm / (grad_norm + 1e-6)
        b1, b2 = self.betas
        for i, (p, master) in enumerate(zip(self.params, self.masters)):
            if p.grad is None:
                continue
            grad = p.grad.detach().to(device='cpu', dtype=torch.float32).mul_(
                self.last_grad_multiplier / self.loss_scale)
            if self.moments[i] is None:
                self.moments[i] = {'step': 0, 'm': torch.zeros_like(master), 'v': torch.zeros_like(master)}
            st = self.moments[i]
            st['step'] += 1
            st['m'].mul_(b1).add_(grad, alpha=1-b1)
            st['v'].mul_(b2).addcmul_(grad, grad, value=1-b2)
            master.mul_(1-self.lr*self.weight_decay)
            denom = st['v'].sqrt().div_(math.sqrt(1-b2**st['step'])).add_(self.eps)
            master.addcdiv_(st['m'], denom, value=-self.lr/(1-b1**st['step']))
            p.copy_(master.to(device=p.device, dtype=p.dtype))
        self.steps += 1
        return True

    def state_dict(self):
        return {'version': 1, 'lr': self.lr, 'loss_scale': self.loss_scale, 'betas': self.betas,
                'eps': self.eps, 'weight_decay': self.weight_decay, 'steps': self.steps,
                'max_grad_norm': self.max_grad_norm,
                'masters': self.masters, 'moments': self.moments}

    @torch.no_grad()
    def load_state_dict(self, state):
        if state.get('version') != 1 or len(state['masters']) != len(self.params) or len(state['moments']) != len(self.params):
            raise ValueError('incompatible optimizer state')
        if any(a.shape != b.shape for a, b in zip(state['masters'], self.params)):
            raise ValueError('optimizer parameter shapes differ')
        if state.get('max_grad_norm') != self.max_grad_norm:
            raise ValueError('gradient clipping policy differs from saved optimizer')
        self.lr, self.loss_scale = state['lr'], state['loss_scale']
        self.betas, self.eps, self.weight_decay, self.steps = state['betas'], state['eps'], state['weight_decay'], state['steps']
        for p, master, saved in zip(self.params, self.masters, state['masters']):
            master.copy_(saved)
            p.copy_(master.to(device=p.device, dtype=p.dtype))
        self.moments = state['moments']
