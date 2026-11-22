"""Controlled extension of a 235M Run 001 checkpoint without resetting weights.

An ordinary --resume can only finish the original fixed step budget.
--extend-steps is an explicit *same-data* continuation after the previous
budget completed. Its default LR is conservative; an optional, recorded text
phase reconfiguration may change accumulation and validation cadence and set
the LR fraction. It does not authorize new corpora or autopromotion.
"""
import math


PHASE_POLICY = 'posta-run001-conservative-extension-v1'
RECONFIGURED_PHASE_POLICY = 'posta-run001-explicit-extension-v2'
EXPANDED_PHASE_POLICY = 'posta-run001-same-corpus-expansion-v3'
RECONFIGURABLE_FIELDS = frozenset({'grad_accum', 'validate_every', 'patience'})
EXPANSION_FIELDS = frozenset({'train_per_group', 'train_selection_sha256'})


def _sha256(value):
    return isinstance(value, str) and len(value) == 64 and all(c in '0123456789abcdef' for c in value)


def _validated_phase(phase, settings, step):
    if phase is None:
        return None
    base_keys = {'policy', 'start_step', 'target_steps', 'start_lr_multiplier'}
    if not isinstance(phase, dict) or (
            set(phase) != base_keys and set(phase) != base_keys | {'changed_settings'}):
        raise ValueError('invalid continuation phase in checkpoint')
    first = phase['start_step']
    end = phase['target_steps']
    scale = phase['start_lr_multiplier']
    policy = phase['policy']
    if (policy not in (PHASE_POLICY, RECONFIGURED_PHASE_POLICY, EXPANDED_PHASE_POLICY) or type(first) is not int or
            type(end) is not int or first < 1 or end <= first or
            end != settings['steps'] or type(scale) is not float or
            not math.isfinite(scale) or not 0 < scale <= (0.1 if policy == PHASE_POLICY else 1.0) or
            not first <= step <= end):
        raise ValueError('invalid continuation phase bounds or learning rate')
    if policy == PHASE_POLICY and set(phase) != base_keys:
        raise ValueError('invalid continuation phase fields')
    if policy in (RECONFIGURED_PHASE_POLICY, EXPANDED_PHASE_POLICY):
        changes = phase.get('changed_settings')
        allowed = RECONFIGURABLE_FIELDS | (EXPANSION_FIELDS if policy == EXPANDED_PHASE_POLICY else frozenset())
        if (not isinstance(changes, dict) or not set(changes) <= allowed):
            raise ValueError('invalid continuation phase changed settings')
        for key, values in changes.items():
            if not isinstance(values, dict) or set(values) != {'from', 'to'}:
                raise ValueError('invalid continuation phase changed settings')
            before, after = values['from'], values['to']
            valid = (_sha256(before) and _sha256(after)) if key == 'train_selection_sha256' else (
                type(before) is int and before >= 1 and type(after) is int and after >= 1)
            if not valid or before == after or after != settings.get(key):
                raise ValueError('invalid continuation phase changed settings')
        if policy == EXPANDED_PHASE_POLICY and (
                'train_per_group' not in changes or
                changes['train_per_group']['to'] <= changes['train_per_group']['from']):
            raise ValueError('invalid continuation phase sample expansion')
    return dict(phase)


def resume_policy(checkpoint, requested_settings, *, extend=False,
                  reconfigure=False, start_lr_multiplier=None, expand_train_sample=False):
    """Return (continuation_phase, actually_extended).

    Strictly preserve provenance and all settings except the step total and,
    when explicitly requested, the small recorded reconfiguration allowlist.
    Extension is legal ONLY at the previous run's exact final step.
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
    if reconfigure and not extend:
        raise ValueError('reconfiguration requires explicit extension')
    if expand_train_sample and not reconfigure:
        raise ValueError('sample expansion requires explicit reconfiguration')
    if start_lr_multiplier is not None and not reconfigure:
        raise ValueError('explicit learning rate requires reconfiguration')
    mutable = (RECONFIGURABLE_FIELDS if reconfigure else frozenset()) | (
        EXPANSION_FIELDS if expand_train_sample else frozenset())
    if {k: v for k, v in existing.items() if k != 'steps' and k not in mutable} != {
            k: v for k, v in requested_settings.items() if k != 'steps' and k not in mutable}:
        raise ValueError('resume settings/provenance changed (only steps may increase)')
    changes = {}
    if reconfigure:
        for key in RECONFIGURABLE_FIELDS:
            before, after = existing.get(key), requested_settings.get(key)
            if type(before) is not int or before < 1 or type(after) is not int or after < 1:
                raise ValueError('invalid reconfigured setting: ' + key)
            if before != after:
                changes[key] = {'from': before, 'to': after}
        if expand_train_sample:
            before, after = existing.get('train_per_group'), requested_settings.get('train_per_group')
            old_sha, new_sha = existing.get('train_selection_sha256'), requested_settings.get('train_selection_sha256')
            if (type(before) is not int or type(after) is not int or before < 1 or after <= before or
                    not _sha256(old_sha) or not _sha256(new_sha)):
                raise ValueError('sample expansion requires a larger cap and valid train fingerprints')
            changes['train_per_group'] = {'from': before, 'to': after}
            if old_sha != new_sha:
                changes['train_selection_sha256'] = {'from': old_sha, 'to': new_sha}
        if (type(start_lr_multiplier) is not float or
                not math.isfinite(start_lr_multiplier) or
                not 0 < start_lr_multiplier <= 1.0):
            raise ValueError('reconfiguration requires a learning rate multiplier in (0, 1]')
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
    if reconfigure:
        return {'policy': EXPANDED_PHASE_POLICY if expand_train_sample else RECONFIGURED_PHASE_POLICY, 'start_step': old_steps,
                'target_steps': new_steps, 'start_lr_multiplier': start_lr_multiplier,
                'changed_settings': changes}, True
    initial_scale = (min(0.1, previous_phase['start_lr_multiplier'] * 0.2)
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
