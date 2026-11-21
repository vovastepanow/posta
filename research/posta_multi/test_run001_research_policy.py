"""Research-only 235M prototype must never claim cleared source rights."""
import unittest
from types import SimpleNamespace

from run001_research_policy import (
    PENDING_FLAGS, ensure_no_unapproved_weights_in_stage2,
    require_local_research_quarantine,
)

REV = 'a' * 40
BASE = {
    'status': 'QUARANTINED: source rights, privacy and benchmark screening required',
    'accepted': 74302,
    'files': [{'path': 'text-train.jsonl'},
              {'path': 'text-validation.jsonl'},
              {'path': 'text-test.jsonl'}],
    'sources': {
        'fineweb2-ru': {'repository': 'HuggingFaceFW/fineweb-2',
                        'revision': REV,
                        'license_review_status': 'pending'},
        'wit': {'repository': 'wikimedia/wit_base',
                'revision': REV,
                'license_review_status': 'pending'},
    },
}


class QuarantinePolicyTests(unittest.TestCase):
    def corpus(self, **changes):
        return SimpleNamespace(manifest={**BASE, **changes}, manifest_hash='b' * 64)

    def test_existing_dataset_eligible_for_local_nonproduction_research(self):
        policy = require_local_research_quarantine(self.corpus())
        self.assertEqual(policy['manifest_sha256'], 'b' * 64)
        self.assertIs(policy['quarantined_research_only'], True)
        self.assertIs(policy['training_approval'], False)
        self.assertIs(policy['private_local_only'], True)
        self.assertIs(policy['no_production_promotion'], True)
        self.assertEqual(PENDING_FLAGS,
            frozenset({'status_not_approved_for_training',
                       'privacy_review_not_passed',
                       'source_rights_review_not_passed'}))

    def test_approved_fake_and_invalid_corpora_cannot_use_research_bypass(self):
        cases = (
            {'status': 'approved_for_training',
             'privacy_review': 'passed', 'source_rights_review': 'passed'},
            {'status': 'quarantined'},
            {'sources': {}},
            {'accepted': 0},
            {'files': []},
            {'sources': {'bad': {'repository': 'x/y', 'revision': 'unverified'}}},
            {'sources': {'mteb-ria-eval': {'repository': 'mteb/RiaNewsRetrieval',
                                          'revision': REV}}},
            {'sources': {'fineweb': {'repository': 'x/y', 'revision': REV,
                                      'license_review_status': 'approved'}}},
        )
        for values in cases:
            with self.subTest(values=values):
                with self.assertRaises(ValueError):
                    require_local_research_quarantine(self.corpus(**values))

    def test_no_stage_two_promotion_with_research_weights(self):
        with self.assertRaisesRegex(ValueError, 'cannot be promoted'):
            ensure_no_unapproved_weights_in_stage2(
                {'quarantined_research_only': True})
        ensure_no_unapproved_weights_in_stage2(
            {'quarantined_research_only': False})

if __name__ == '__main__':
    unittest.main()
