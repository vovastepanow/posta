import copy
import unittest
import torch
from cpu_master_adam import CPUMasterAdamW


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
