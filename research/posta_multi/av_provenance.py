"""Fail-closed provenance checks for audiovisual research source weights."""
from run001_retrieval_loss import checkpoint_is_healthy


def validate_av_source(state, manifest_sha, tokenizer_sha):
    settings = state.get('settings', {})
    source_format = state.get('format')
    text_best = 'posta-multi-235m-run001-text-v3-best'
    retrieval_best = 'posta-multi-235m-run001-retrieval-v1-best'
    if (source_format not in (text_best, retrieval_best) or
            settings.get('manifest_sha256') != manifest_sha or
            settings.get('tokenizer_sha256') != tokenizer_sha or
            settings.get('quarantined_research_only') is not True or
            settings.get('private_local_only') is not True or
            settings.get('production_promotion_allowed') is not False or
            type(state.get('step')) is not int or state['step'] < 1):
        raise ValueError('source checkpoint provenance or quarantine mismatch')
    if source_format == retrieval_best and (
            state.get('selection_policy') != 'healthy-max-mrr-v2' or
            not checkpoint_is_healthy(state.get('metrics'))):
        raise ValueError('retrieval source has not passed its health checks')
    return settings
