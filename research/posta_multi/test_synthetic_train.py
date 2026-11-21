"""Verify the synthetic training checkpoint resumes without changing results."""
import tempfile
import unittest
from pathlib import Path
import torch
from synthetic_train import run


class SyntheticTrainingTests(unittest.TestCase):
    def test_resume_matches_uninterrupted_training(self):
        with tempfile.TemporaryDirectory() as directory:
            full = run(4, Path(directory) / 'full.pt')
            run(2, Path(directory) / 'resumed.pt')
            resumed = run(4, Path(directory) / 'resumed.pt', resume=True)
            self.assertEqual(resumed['step'], 4)
            for key in full['model']:
                self.assertTrue(torch.equal(full['model'][key], resumed['model'][key]), key)
            self.assertTrue(torch.equal(full['torch_rng'], resumed['torch_rng']))
            with self.assertRaises(FileExistsError):
                run(1, Path(directory) / 'full.pt')
            with self.assertRaises(ValueError):
                run(3, Path(directory) / 'resumed.pt', resume=True)


if __name__ == '__main__':
    unittest.main()
