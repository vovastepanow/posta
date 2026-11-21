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
