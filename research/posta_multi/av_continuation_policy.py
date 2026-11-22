"""Deterministic, quarantine-preserving scheduling for sustained AV training.

Pure policy: no GPU launch, no dataset mutation, no deployment or model
promotion. Optimizer state and the source checkpoint are handled by trainer.
"""
import hashlib
import math

POOL_SIZES = (32, 16, 8, 4, 2)
MODALITIES = ("video_text", "speech_text")


def select_training_pools(corpus):
    """Choose the largest available disjoint training-only media pools."""
    from av_data import select_media_pairs

    pools = {}
    hashes = {}
    for kind in MODALITIES:
        errors = []
        for amount in POOL_SIZES:
            try:
                rows, signature = select_media_pairs(corpus, kind, "train", amount)
            except ValueError as error:
                # Lack of enough isolated examples is expected when lowering
                # the cap; other validation problems should not be swallowed.
                if not str(error).startswith("only "):
                    raise
                errors.append(str(error))
                continue
            pools[kind] = rows
            hashes[kind] = signature
            break
        else:
            raise ValueError(f"insufficient isolated training {kind}: {errors[-1] if errors else ''}")
    return pools, hashes


def pair_indices(pool_size, step, *, batch_size=2):
    """Stable, distinct rotating pairs with full coverage across epochs."""
    if (type(pool_size) is not int or pool_size < batch_size or
            type(batch_size) is not int or batch_size < 2 or
            type(step) is not int or step <= 0):
        raise ValueError("invalid AV batch scheduling")
    # An epoch is ceil(pool_size / batch_size) steps. Generate one
    # deterministic permutation per epoch, and rotate the overflow.
    epoch_length = math.ceil(pool_size / batch_size)
    epoch, index = divmod(step - 1, epoch_length)
    order = list(range(pool_size))
    order.sort(key=lambda position: hashlib.sha256(
        f"posta-av-epoch-{epoch}-{position}".encode("ascii")).digest())
    start = index * batch_size
    result = order[start:start + batch_size]
    if len(result) < batch_size:
        for offset in order:
            if offset not in result:
                result.append(offset)
            if len(result) == batch_size:
                break
    return result


def validated_progress(state, *, expected_step_cap, source_hash, manifest_hash,
                       tokenizer_hash, pool_hashes):
    """Reject incompatible or manipulated continuation checkpoints."""
    if (type(state) is not dict or
            state.get("format") != "posta-av-235m-continuation-v1" or
            state.get("production_promotion_allowed") is not False or
            state.get("private_local_only") is not True or
            state.get("quarantined_research_only") is not True or
            state.get("source_checkpoint_sha256") != source_hash or
            state.get("manifest_sha256") != manifest_hash or
            state.get("tokenizer_sha256") != tokenizer_hash or
            state.get("pool_sha256") != pool_hashes or
            state.get("step_cap") != expected_step_cap or
            type(state.get("step")) is not int or
            not 1 <= state["step"] <= expected_step_cap or
            "model" not in state or "optimizer" not in state):
        raise ValueError("incompatible private AV continuation checkpoint")
    return state["step"]
