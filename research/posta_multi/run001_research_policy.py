"""Fail-closed scope checks for NON-PRODUCTION local research on existing data.

This is NOT permission clearance and never changes a dataset manifest.
No releases, production promotion, sharing of checkpoints, or treatment of
quarantined texts as approved training data is authorized by this module.
"""
from run001_data import approval_issues

PENDING_FLAGS = frozenset({
    'status_not_approved_for_training',
    'privacy_review_not_passed',
    'source_rights_review_not_passed',
})

EVALUATION_REPOS = (
    'VLM2Vec/MMEB-V3',
    'mteb/RiaNewsRetrieval',
    'miracl/miracl',
    'miracl/nanomiracl',
)


def require_local_research_quarantine(corpus):
    manifest = corpus.manifest
    if not isinstance(manifest, dict):
        raise ValueError('local research requires a valid quarantined manifest')
    status = manifest.get('status')
    if not isinstance(status, str) or not status.startswith('QUARANTINED:'):
        raise ValueError('local research requires a specifically quarantined local corpus')
    issues = set(approval_issues(manifest))
    if issues != PENDING_FLAGS:
        raise ValueError('local research requires all three explicit review blockers')
    sources = manifest.get('sources')
    rows = manifest.get('accepted')
    if not isinstance(sources, dict) or not sources or type(rows) is not int or rows < 128:
        raise ValueError('local research requires populated pinned sources')
    files = manifest.get('files')
    if not isinstance(files, list) or len(files) < 3:
        raise ValueError('local research requires split-separated source files')
    for identifier, entry in sources.items():
        if not isinstance(identifier, str) or not isinstance(entry, dict):
            raise ValueError('invalid source provenance in quarantine')
        repository = entry.get('repository')
        revision = entry.get('revision')
        if (not isinstance(repository, str) or not repository or
                not isinstance(revision, str) or len(revision) != 40 or
                any(c not in '0123456789abcdef' for c in revision)):
            raise ValueError('missing pinned source provenance')
        if repository in EVALUATION_REPOS or identifier.startswith(('mteb-', 'mmeb-', 'nanomiracl-')):
            raise ValueError('evaluation-only data is never allowed in research')
        if entry.get('license_review_status') not in ('pending', None):
            raise ValueError('unknown source license review state')
    return {
        'quarantined_research_only': True,
        'training_approval': False,
        'manifest_sha256': corpus.manifest_hash,
        'private_local_only': True,
        'no_production_promotion': True,
    }


def ensure_no_unapproved_weights_in_stage2(settings):
    if settings.get('quarantined_research_only'):
        raise ValueError('quarantined research weights cannot be promoted to stage 2')
