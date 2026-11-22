import unittest
from dataclasses import replace

import torch

from av_alignment import alignment_backward, configure_av_trainable
from model import PostaMulti
from synthetic_train import tiny_config


class AlignmentTests(unittest.TestCase):
    def test_real_objective_reaches_both_media_paths_without_updating_text(self):
        torch.manual_seed(5)
        torch.set_num_threads(2)
        config = replace(tiny_config(), temporal_length=64)
        model = PostaMulti(config, dropout=0)
        configure_av_trainable(model)
        targets = torch.nn.functional.normalize(torch.randn(4, config.embed_dim), dim=-1)
        videos = [(targets[i], [(torch.rand(1, 3, 3, config.image_size, config.image_size),
                                 torch.tensor([[0., .1, .2]])),
                                (torch.rand(1, 3, 3, config.image_size, config.image_size),
                                 torch.tensor([[.3, .4, .5]]))]) for i in range(2)]
        speech = [(targets[i+2], torch.rand(1, config.mel_bins, 24)) for i in range(2)]
        loss, stats = alignment_backward(model, videos, speech, 'cpu',
                                         time_encoding='relative-centered-v1',
                                         window_aggregation='hierarchical-v1')
        self.assertTrue(torch.isfinite(torch.tensor(loss)))
        self.assertEqual(set(stats), {'video', 'speech'})
        for parameter in (model.image_patches.weight, model.audio_patch_projection.weight,
                          model.temporal_encoder.layers[0].linear1.weight):
            self.assertIsNotNone(parameter.grad)
            self.assertTrue(torch.isfinite(parameter.grad).all())
            self.assertGreater(float(parameter.grad.abs().sum()), 0)
        self.assertIsNone(model.text_tokens.weight.grad)
        self.assertIsNone(model.fusion_cls.grad)

    def test_teacher_geometry_reaches_audio_and_temporal_gradients(self):
        torch.manual_seed(27)
        config = replace(tiny_config(), temporal_length=64)
        model = PostaMulti(config, dropout=0)
        configure_av_trainable(model)
        targets = torch.nn.functional.normalize(torch.randn(4, config.embed_dim), dim=-1)
        speech = [(targets[i], torch.rand(1, config.mel_bins, 24))
                  for i in range(4)]
        loss, stats = alignment_backward(
            model, [], speech, 'cpu', teacher_geometry_weight=0.5)
        self.assertGreater(loss, 0)
        self.assertGreater(stats['speech']['teacher_geometry_loss'], 0)
        self.assertIsNotNone(model.audio_patch_projection.weight.grad)
        self.assertTrue(torch.isfinite(model.audio_patch_projection.weight.grad).all())
        self.assertGreater(float(model.audio_patch_projection.weight.grad.abs().sum()), 0)
        self.assertIsNone(model.text_tokens.weight.grad)

    def test_single_pair_and_training_mode_rejected(self):
        model = PostaMulti(tiny_config(), dropout=0)
        configure_av_trainable(model)
        with self.assertRaisesRegex(ValueError, 'at least two'):
            alignment_backward(model, [(torch.randn(model.config.embed_dim), [])], [], 'cpu')
        model.train()
        with self.assertRaisesRegex(ValueError, 'eval mode'):
            alignment_backward(model, [], [], 'cpu')


if __name__ == '__main__':
    unittest.main()
