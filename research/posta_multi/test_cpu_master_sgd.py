import copy
import unittest
import torch
from cpu_master_sgd import CPUMasterSGD


class MasterSGDTests(unittest.TestCase):
    def test_update_and_resume(self):
        p = torch.nn.Parameter(torch.tensor([1., -2.], dtype=torch.float16))
        opt = CPUMasterSGD([p], lr=0.1, loss_scale=16)
        opt.backward((p.float() ** 2).sum())
        self.assertTrue(opt.step())
        self.assertTrue(torch.allclose(p.float(), torch.tensor([0.8, -1.6]), atol=0.001))
        checkpoint = copy.deepcopy(opt.state_dict())
        other = torch.nn.Parameter(torch.zeros(2, dtype=torch.float16))
        restored = CPUMasterSGD([other])
        restored.load_state_dict(checkpoint)
        self.assertTrue(torch.equal(other, p))
        self.assertEqual(restored.steps, 1)
        self.assertTrue(torch.equal(restored.masters[0], opt.masters[0]))
        opt.zero_grad()
        restored.zero_grad()
        opt.backward((p.float() ** 2).sum())
        restored.backward((other.float() ** 2).sum())
        self.assertTrue(opt.step())
        self.assertTrue(restored.step())
        self.assertTrue(torch.equal(p, other))
        self.assertTrue(torch.equal(opt.masters[0], restored.masters[0]))

    def test_overflow_skips_all_updates(self):
        a = torch.nn.Parameter(torch.tensor([1.], dtype=torch.float16))
        b = torch.nn.Parameter(torch.tensor([2.], dtype=torch.float16))
        opt = CPUMasterSGD([a, b])
        a.grad = torch.tensor([1.], dtype=torch.float16)
        b.grad = torch.tensor([float('inf')], dtype=torch.float16)
        self.assertFalse(opt.step())
        self.assertEqual(a.item(), 1.)
        self.assertEqual(b.item(), 2.)
        self.assertEqual(opt.steps, 0)
