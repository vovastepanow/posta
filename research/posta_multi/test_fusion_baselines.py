"""CPU-only tests for honest fusion baselines on trained embedding spaces."""
import unittest

import torch

from fusion_baselines import masked_average_fusion, single_modality_fusion_probe
from model import PostaMulti
from synthetic_train import tiny_config


class MaskedAverageFusionTests(unittest.TestCase):
    def test_single_present_modality_passes_through_unchanged(self):
        torch.manual_seed(333)
        embeddings = torch.randn(4, 4, 16)
        for slot in range(4):
            mask = torch.zeros(4, 4, dtype=torch.bool)
            mask[:, slot] = True
            result = masked_average_fusion(embeddings, mask)
            expected = torch.nn.functional.normalize(embeddings[:, slot], dim=1)
            self.assertTrue(torch.allclose(result, expected, atol=1e-6))

    def test_absent_modalities_do_not_affect_result(self):
        embeddings = torch.tensor(
            [[[3., 4.], [2., 0.], [0., 99.], [float("nan"), float("nan")]]])
        mask = torch.tensor([[True, True, False, False]])
        one = masked_average_fusion(embeddings, mask, weights=[2, 1, 1, 1])
        embeddings[0, 3] = torch.tensor([999999., 999999.])
        two = masked_average_fusion(embeddings, mask, weights=[2, 1, 1, 1])
        self.assertTrue(torch.allclose(one, two, atol=1e-7))
        self.assertTrue(torch.isfinite(one).all())
        self.assertAlmostEqual(float(one.norm()), 1, places=6)

    def test_single_modality_probe_never_includes_query_text(self):
        torch.manual_seed(8)
        model = PostaMulti(tiny_config(), dropout=0).eval()
        videos = torch.randn(3, model.config.embed_dim)
        direct, fused = single_modality_fusion_probe(model, videos, "video")
        self.assertTrue(torch.allclose(direct, torch.nn.functional.normalize(videos, dim=1),
                                       atol=1e-6))
        self.assertEqual(tuple(fused.shape), tuple(direct.shape))
        self.assertTrue(torch.isfinite(fused).all())
        with self.assertRaisesRegex(ValueError, "expected image"):
            single_modality_fusion_probe(model, videos, "text")

    def test_present_bad_vectors_or_bad_weights_rejected(self):
        inputs = torch.ones(2, 4, 3)
        mask = torch.ones(2, 4, dtype=torch.bool)
        for weights in ([1., -1., 1., 1.], [0., 1., 1., 1.],
                        [1., float("inf"), 1., 1.], [1., 1.]):
            with self.subTest(weights=weights):
                with self.assertRaises(ValueError):
                    masked_average_fusion(inputs, mask, weights)
        inputs[0, 2] = 0
        with self.assertRaisesRegex(ValueError, "zero"):
            masked_average_fusion(inputs, mask)
        inputs[0, 2] = float("nan")
        with self.assertRaisesRegex(ValueError, "nonfinite"):
            masked_average_fusion(inputs, mask)
        with self.assertRaisesRegex(ValueError, "boolean"):
            masked_average_fusion(inputs.nan_to_num(), mask.float())


if __name__ == "__main__":
    unittest.main()
