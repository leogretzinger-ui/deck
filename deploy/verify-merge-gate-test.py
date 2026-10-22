import copy
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("merge_gate", Path(__file__).with_name("verify-merge-gate.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class ConditionalChecks(unittest.TestCase):
    def setUp(self):
        self.head = "a" * 40
        self.run = {"head_sha": self.head, "path": ".github/workflows/ci-fast.yaml",
                    "event": "pull_request", "status": "completed", "conclusion": "success",
                    "pull_requests": [{"number": 1}]}
        self.checks = [
            {"name": "fast-checks", "conclusion": "SUCCESS", "workflowName": "Fast checks",
             "detailsUrl": "https://github.com/org/deck/actions/runs/7/job/10"},
            {"name": "probes", "conclusion": "SKIPPED", "workflowName": "Fast checks",
             "detailsUrl": "https://github.com/org/deck/actions/runs/7/job/11"},
        ]

    def verify(self, checks=None, run=None):
        with patch.object(gate, "gh", return_value=self.run if run is None else run):
            return gate.check_failures(self.checks if checks is None else checks, self.head, 1, "org", "deck")

    def test_same_run_exact_pr_allows_intentional_skip(self):
        self.assertEqual(self.verify(), [])
        for name in ["source-export", "source-export (parallel)", "probes (gateway)"]:
            self.checks[1]["name"] = name
            self.assertEqual(self.verify(), [])

    def test_failures_pending_and_unknown_skips_still_block(self):
        for state in ["FAILURE", "CANCELLED", "TIMED_OUT", "QUEUED", "IN_PROGRESS", None]:
            checks = copy.deepcopy(self.checks)
            checks[1]["conclusion"] = state
            self.assertTrue(self.verify(checks))
        for name in ["scope", "documentation", "fast-checks", "other-security-check"]:
            checks = copy.deepcopy(self.checks)
            checks[1]["name"] = name
            self.assertTrue(self.verify(checks))
        self.assertTrue(self.verify([]))

    def test_wrong_workflow_head_pr_or_event_blocks(self):
        for key, value in [("head_sha", "b" * 40), ("path", ".github/workflows/fake.yaml"),
                           ("event", "push"), ("status", "in_progress"),
                           ("conclusion", "failure"), ("pull_requests", [{"number": 2}])]:
            run = dict(self.run, **{key: value})
            self.assertTrue(self.verify(run=run))
        checks = copy.deepcopy(self.checks)
        checks[1]["detailsUrl"] = "https://github.com/org/deck/actions/runs/8/job/11"
        self.assertTrue(self.verify(checks))
        for key,value in [("workflowName", "Fake checks"), ("detailsUrl", "https://evil.test/run/7")]:
            checks = copy.deepcopy(self.checks)
            checks[1][key] = value
            with self.assertRaises(RuntimeError): self.verify(checks)

    def test_review_protection_is_not_relaxed(self):
        review = {"author": {"login": "noise-review", "__typename": "Bot"},
                  "state": "APPROVED", "submittedAt": "2026-10-18T00:00:00Z",
                  "body": "APPROVE\n\nReviewed head: " + self.head}
        self.assertEqual(gate.verdict_failures([review], self.head, "noise-review[bot]"), [])
        for change in [{"state": "CHANGES_REQUESTED"}, {"body": "Approved without a marker"},
                       {"body": "Reviewed head: " + "b" * 40},
                       {"author": {"login": "noise-review", "__typename": "User"}}]:
            self.assertTrue(gate.verdict_failures([dict(review, **change)], self.head, "noise-review[bot]"))


if __name__ == "__main__":
    unittest.main()
