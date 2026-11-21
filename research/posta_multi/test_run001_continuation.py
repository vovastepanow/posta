"""No-GPU tests for safe resume and explicit extension of 235M runs."""
import math
import unittest

from run001_continuation import (PHASE_POLICY, extension_lr_multiplier,
                                  resume_policy)


SETTINGS = {
    'format': 'posta-multi-235m-run001-text-v3',
    'steps': 100,
    'manifest_sha256': 'a' * 64,
    'tokenizer_sha256': 'b' * 64,
    'config': {'vocab_size': 65536, 'text_pooling': 'mean'},
    'lr': 0.0001,
    'warmup': 10,
    'seed': 2026,
    'validation_metric': 'macro_mean_loss_across_groups',
}


def checkpoint(step, *, settings=None, phase=None):
    info = {'step': step, 'settings': dict(SETTINGS if settings is None
                                         else settings)}
    if phase is not None:
        info['continuation_phase'] = phase
    return info


def with_steps(n):
    return {**SETTINGS, 'steps': n}


class ResumeContinuationTests(unittest.TestCase):
    def test_identical_settings_preserve_unfinished_run_without_extension(self):
        phase, extended = resume_policy(checkpoint(37), dict(SETTINGS))
        self.assertIsNone(phase)
        self.assertFalse(extended)
        self.assertEqual(extension_lr_multiplier(38, phase), 1.0)

    def test_final_step_requires_explicit_larger_budget(self):
        finished = checkpoint(100)
        with self.assertRaisesRegex(ValueError, 'explicit extension'):
            resume_policy(finished, SETTINGS)
        with self.assertRaisesRegex(ValueError, 'requires --extend-steps'):
            resume_policy(finished, with_steps(120))
        phase, extended = resume_policy(finished, with_steps(120), extend=True)
        self.assertTrue(extended)
        self.assertEqual(phase, {
            'policy': PHASE_POLICY, 'start_step': 100,
            'target_steps': 120, 'start_lr_multiplier': 0.1,
        })
        self.assertGreater(extension_lr_multiplier(101, phase),
                           extension_lr_multiplier(120, phase))
        self.assertLessEqual(extension_lr_multiplier(101, phase), 0.1)
        self.assertAlmostEqual(extension_lr_multiplier(120, phase), 0.02)

    def test_restart_middle_of_extension_reuses_exact_lr_phase(self):
        phase, _ = resume_policy(checkpoint(100), with_steps(125), extend=True)
        pending = checkpoint(111, settings=with_steps(125), phase=phase)
        restored, extended = resume_policy(pending, with_steps(125))
        self.assertFalse(extended)
        self.assertEqual(restored, phase)
        self.assertEqual(extension_lr_multiplier(112, restored),
                         extension_lr_multiplier(112, phase))
        with self.assertRaisesRegex(ValueError, 'finish the existing'):
            resume_policy(pending, with_steps(150), extend=True)

    def test_multiple_extensions_decay_again_instead_of_rewarming(self):
        first, _ = resume_policy(checkpoint(100), with_steps(150), extend=True)
        second, extended = resume_policy(
            checkpoint(150, settings=with_steps(150), phase=first),
            with_steps(180), extend=True)
        self.assertTrue(extended)
        self.assertAlmostEqual(second['start_lr_multiplier'], 0.02)
        self.assertAlmostEqual(extension_lr_multiplier(180, second), 0.004)
        self.assertLess(extension_lr_multiplier(151, second),
                        extension_lr_multiplier(150, first))

    def test_refuse_changed_corpus_tokenizer_architecture_optimizer_and_schedule(self):
        finished = checkpoint(100)
        for changed in (
                {'manifest_sha256': 'z' * 64},
                {'tokenizer_sha256': 'z' * 64},
                {'config': {'vocab_size': 65536, 'text_pooling': 'mean-residual'}},
                {'lr': 0.00002},
                {'seed': 7},
                {'warmup': 20},
                {'validation_metric': 'mean_loss'}):
            requested = {**with_steps(130), **changed}
            with self.subTest(changed=changed):
                with self.assertRaisesRegex(ValueError, 'settings/provenance changed'):
                    resume_policy(finished, requested, extend=True)

    def test_refuse_extension_while_original_steps_unfinished_or_unchanged(self):
        with self.assertRaisesRegex(ValueError, 'finish the existing'):
            resume_policy(checkpoint(99), with_steps(140), extend=True)
        with self.assertRaisesRegex(ValueError, 'larger --steps'):
            resume_policy(checkpoint(100), SETTINGS, extend=True)
        with self.assertRaisesRegex(ValueError, 'cannot reduce'):
            resume_policy(checkpoint(70), with_steps(90), extend=True)

    def test_tampered_checkpoint_cursor_phase_and_lr_rejected(self):
        for bad in (True, -1, 101, 7.5):
            with self.subTest(bad=bad):
                with self.assertRaisesRegex(ValueError, 'cursor'):
                    resume_policy(checkpoint(bad), SETTINGS)
        old_phase, _ = resume_policy(checkpoint(100), with_steps(120), extend=True)
        for malformed in (
            {**old_phase, 'target_steps': 140},
            {**old_phase, 'start_lr_multiplier': float('nan')},
            {**old_phase, 'start_lr_multiplier': 0.9},
            {**old_phase, 'policy': 'unknown'},
            {**old_phase, 'extra_field': True},
        ):
            with self.subTest(malformed=malformed):
                with self.assertRaisesRegex(ValueError, 'invalid continuation phase'):
                    resume_policy(checkpoint(101, settings=with_steps(120),
                                             phase=malformed), with_steps(120))
        with self.assertRaisesRegex(ValueError, 'outside continuation'):
            extension_lr_multiplier(100, old_phase)
        with self.assertRaisesRegex(ValueError, 'outside continuation'):
            extension_lr_multiplier(121, old_phase)


if __name__ == '__main__':
    unittest.main()
