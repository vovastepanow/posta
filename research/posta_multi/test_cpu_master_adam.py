import copy
import tempfile
from pathlib import Path
import unittest
import torch
from cpu_master_adam import CPUMasterAdamW
from train_text import save_atomic


class MasterAdamTests(unittest.TestCase):
    def test_matches_float32_adamw_and_resumes(self):
        p = torch.nn.Parameter(torch.tensor([1., -2.], dtype=torch.float16))
        q = torch.nn.Parameter(p.detach().float().clone())
        optim = CPUMasterAdamW([p], lr=0.001, loss_scale=16)
        ref = torch.optim.AdamW([q], lr=0.001)
        for _ in range(3):
            optim.zero_grad(); ref.zero_grad()
            optim.backward((p.float() ** 2).sum()); (q ** 2).sum().backward()
            self.assertTrue(optim.step()); ref.step()
        self.assertTrue(torch.allclose(optim.masters[0], q.detach(), atol=2e-5))
        state = copy.deepcopy(optim.state_dict())
        other = torch.nn.Parameter(torch.zeros(2, dtype=torch.float16))
        restored = CPUMasterAdamW([other])
        restored.load_state_dict(state)
        self.assertTrue(torch.equal(p, other))
        self.assertEqual(restored.steps, 3)
        p.grad = torch.tensor([float('inf'), 1.], dtype=torch.float16)
        before = p.detach().clone()
        self.assertFalse(optim.step())
        self.assertTrue(torch.equal(p, before))

    def test_clipping_limits_updates_and_reports_true_global_norm(self):
        p = torch.nn.Parameter(torch.tensor([0.5, -1.0], dtype=torch.float16))
        q = torch.nn.Parameter(p.detach().float().clone())
        optimizer = CPUMasterAdamW([p], lr=0.001, loss_scale=16,
                                   max_grad_norm=1.0)
        ref = torch.optim.AdamW([q], lr=0.001)
        gradients = torch.tensor([3.0, 4.0])
        p.grad = (gradients * 16).half()
        q.grad = gradients / gradients.norm()
        self.assertTrue(optimizer.step())
        ref.step()
        self.assertAlmostEqual(optimizer.last_grad_norm, 5.0, places=5)
        self.assertAlmostEqual(optimizer.last_grad_multiplier, 0.2, places=5)
        self.assertTrue(torch.allclose(optimizer.masters[0], q.detach(),
                                       atol=2e-6, rtol=2e-6))
        state = copy.deepcopy(optimizer.state_dict())
        restored_param = torch.nn.Parameter(torch.zeros_like(p))
        restored = CPUMasterAdamW([restored_param], max_grad_norm=1.0)
        restored.load_state_dict(state)
        self.assertTrue(torch.equal(restored_param, p))
        with self.assertRaisesRegex(ValueError, 'clipping policy'):
            CPUMasterAdamW([torch.nn.Parameter(p.detach().clone())]).load_state_dict(state)

    def test_missing_grad_and_invalid_clip_refused(self):
        p = torch.nn.Parameter(torch.ones(2, dtype=torch.float16))
        with self.assertRaises(ValueError):
            CPUMasterAdamW([p], max_grad_norm=float('inf'))
        with self.assertRaises(ValueError):
            CPUMasterAdamW([p], max_grad_norm=0)
        optim = CPUMasterAdamW([p], max_grad_norm=1)
        self.assertFalse(optim.step())
        self.assertEqual(optim.steps, 0)
        self.assertTrue(torch.equal(p, torch.ones_like(p)))



class MasterAdamRecoveryTests(unittest.TestCase):
    def test_atomic_resume_reproduces_uninterrupted_fp16_master_update(self):
        """Checkpoint must preserve FP32 master weights, Adam moments and sampling RNG."""
        torch.manual_seed(1001)
        param = torch.nn.Parameter(torch.tensor([0.5, -1.0, 1.5], dtype=torch.float16))
        optim = CPUMasterAdamW([param], lr=0.0005, loss_scale=16,
                               max_grad_norm=1.0)
        sampler = torch.Generator(device='cpu').manual_seed(411)

        def update(p, opt, rng):
            target = torch.randn(3, generator=rng)
            opt.zero_grad()
            opt.backward(((p.float() - target) ** 2).mean())
            self.assertTrue(opt.step())
            return target

        update(param, optim, sampler)
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / 'checkpoint.pt'
            save_atomic(output, {
                'param': param.detach(),
                'optimizer': optim.state_dict(),
                'sample_state': sampler.get_state(),
                'rng': torch.get_rng_state(),
            })
            saved = torch.load(output, map_location='cpu', weights_only=True)
            target_full = update(param, optim, sampler)
            full_master = optim.masters[0].clone()
            full_param = param.detach().clone()
            full_moment = copy.deepcopy(optim.moments[0])

            recovered = torch.nn.Parameter(torch.zeros_like(param))
            resumed_optimizer = CPUMasterAdamW([recovered], lr=0.0005,
                                               loss_scale=16, max_grad_norm=1.0)
            resumed_optimizer.load_state_dict(saved['optimizer'])
            recovered_sampler = torch.Generator().set_state(saved['sample_state'])
            torch.set_rng_state(saved['rng'])
            target_resumed = update(recovered, resumed_optimizer, recovered_sampler)
            self.assertTrue(torch.equal(target_full, target_resumed))
            self.assertTrue(torch.equal(recovered, full_param))
            self.assertTrue(torch.equal(resumed_optimizer.masters[0], full_master))
            self.assertEqual(resumed_optimizer.steps, 2)
            self.assertTrue(torch.equal(resumed_optimizer.moments[0]['m'],
                                        full_moment['m']))
            self.assertTrue(torch.equal(resumed_optimizer.moments[0]['v'],
                                        full_moment['v']))

    def test_clipping_preserves_order_independent_multi_parameter_global_norm(self):
        a = torch.nn.Parameter(torch.tensor([0., 0.], dtype=torch.float16))
        b = torch.nn.Parameter(torch.tensor([0., 0.], dtype=torch.float16))
        opt = CPUMasterAdamW([a, b], lr=0.001, loss_scale=16,
                             max_grad_norm=1.0)
        a.grad = torch.tensor([3*16., 4*16.], dtype=torch.float16)
        b.grad = torch.tensor([12*16., 0.], dtype=torch.float16)
        self.assertTrue(opt.step())
        self.assertAlmostEqual(opt.last_grad_norm, 13.0, places=5)
        self.assertAlmostEqual(opt.last_grad_multiplier, 1/13, places=5)
