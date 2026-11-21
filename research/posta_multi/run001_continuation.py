"""Controlled extension of a 235M Run 001 checkpoint without resetting weights.

An ordinary --resume can only finish the original fixed step budget.
--extend-steps is an explicit, lower-LR *same-data* continuation after the
previous budget completed. It does not authorize new corpora or autopromotion.
"""
import math


PHASE_POLICY = 'posta-run001-conservative-extension-v1'


def _validated_phase(phase, settings, step):
    if phase is None:
        return None
    if not isinstance(phase, dict) or set(phase) != {
            'policy', 'start_step', 'target_steps', 'start_lr_multiplier'}:
        raise ValueError('invalid continuation phase in checkpoint')
    first = phase['start_step']
    end = phase['target_steps']
    scale = phase['start_lr_multiplier']
    if (phase['policy'] != PHASE_POLICY or type(first) is not int or
            type(end) is not int or first < 1 or end <= first or
            end != settings['steps'] or type(scale) is not float or
            not math.isfinite(scale) or not 0 < scale <= 0.1 or
            not first <= step <= end):
        raise ValueError('invalid continuation phase bounds or learning rate')
    return dict(phase)


def resume_policy(checkpoint, requested_settings, *, extend=False):
    """Return (continuation_phase, actually_extended).

    Strictly preserve every old setting except an explicitly increased step
    total. Extension is legal ONLY at the previous run's exact final step.
    """
    existing = checkpoint.get('settings')
    if not isinstance(existing, dict) or not isinstance(requested_settings, dict):
        raise ValueError('invalid checkpoint or requested settings')
    old_steps = existing.get('steps')
    step = checkpoint.get('step')
    new_steps = requested_settings.get('steps')
    if (type(step) is not int or type(old_steps) is not int or
            type(new_steps) is not int or old_steps < 1 or new_steps < 1 or
            not 0 <= step <= old_steps):
        raise ValueError('invalid checkpoint step budget or cursor')
    previous_phase = _validated_phase(
        checkpoint.get('continuation_phase'), existing, step)
    # Never relax corpus, tokenizer, model, quality criteria or other settings.
    if {k: v for k, v in existing.items() if k != 'steps'} != {
            k: v for k, v in requested_settings.items() if k != 'steps'}:
        raise ValueError('resume settings/provenance changed (only steps may increase)')
    if new_steps == old_steps:
        if extend:
            raise ValueError('--extend-steps requires a larger --steps target')
        if step == old_steps:
            raise ValueError('step budget completed; explicit extension is required')
        return previous_phase, False
    if new_steps < old_steps:
        raise ValueError('cannot reduce the step budget on resume')
    if not extend:
        raise ValueError('larger step target requires --extend-steps')
    if step != old_steps:
        raise ValueError('finish the existing step budget before extending')
    initial_scale = (previous_phase['start_lr_multiplier'] * 0.2
                     if previous_phase is not None else 0.1)
    return {
        'policy': PHASE_POLICY,
        'start_step': old_steps,
        'target_steps': new_steps,
        'start_lr_multiplier': float(initial_scale),
    }, True


def extension_lr_multiplier(step, phase):
    """Decay within the extension, never ramp back to initial scratch LR."""
    if phase is None:
        return 1.0
    if (type(step) is not int or
            not phase['start_step'] < step <= phase['target_steps']):
        raise ValueError('step outside continuation phase')
    fraction = ((step - phase['start_step']) /
                (phase['target_steps'] - phase['start_step']))
    return phase['start_lr_multiplier'] * (
        0.2 + 0.8 * 0.5 * (1 + math.cos(math.pi * fraction)))
