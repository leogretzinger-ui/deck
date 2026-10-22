import copy
import io
import json
import os
import unittest
import zipfile
from unittest.mock import patch

import ci_scope
import jev_plan


def fixture():
    probabilities = {name: .1 for name in jev_plan.QUESTIONS}
    probabilities.update(frontend=.87, **{"browser-ui": .82, "deploy-suite": .48})
    return {"schema": 1, "repository": "org/deck", "target_run_id": 7,
            "event": "pull_request", "base_sha": "b"*40, "head_sha": "a"*40,
            "planner_source_sha": "c"*40, "model": jev_plan.MODEL, "threshold": .5,
            "mode": "selective", "probabilities": probabilities,
            "jobs": ["documentation", "frontend", "probes"], "probe_shards": [1, 2, 3]}


class PlanTests(unittest.TestCase):
    def validate(self, value):
        return jev_plan.validate(value, "org/deck", "7", "pull_request", "b"*40, "a"*40, "c"*40)

    def test_ui_selection_and_documentation_minimum(self):
        selected = self.validate(fixture())
        self.assertEqual(selected["selector"], "jev")
        self.assertEqual(selected["jobs"], ["documentation", "frontend", "probes"])
        self.assertEqual(selected["probe_shards"], [1, 2, 3])

    def test_plan_bindings_and_decisions_cannot_be_changed(self):
        cases = {"repository": "other/deck", "target_run_id": 8, "event": "merge_group",
                 "base_sha": "d"*40, "head_sha": "d"*40, "planner_source_sha": "d"*40,
                 "schema": 2, "model": "other", "threshold": .8, "mode": "full",
                 "jobs": ["documentation"], "probe_shards": ["gateway"]}
        for name, value in cases.items():
            with self.subTest(name=name), self.assertRaises(ValueError):
                plan = fixture(); plan[name] = value; self.validate(plan)
        for value in [True, -1, 2, float("nan"), float("inf"), "0.2"]:
            with self.subTest(probability=value), self.assertRaises(ValueError):
                plan = fixture(); plan["probabilities"]["frontend"] = value; self.validate(plan)

    def test_unknown_and_missing_questions_refused(self):
        for name in ("extra", "frontend"):
            plan = fixture()
            if name == "extra": plan["probabilities"][name] = .1
            else: del plan["probabilities"][name]
            with self.assertRaises(ValueError): self.validate(plan)

    def test_trusted_publisher_and_safe_archive(self):
        publisher = {"path": ".github/workflows/ci-advisor.yaml", "event": "workflow_run",
                     "head_branch": "main", "head_sha": "c"*40, "status": "completed", "conclusion": "success",
                     "repository": {"full_name": "org/deck"}, "head_repository": {"full_name": "org/deck"}}
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive: archive.writestr("plan.json", json.dumps(fixture()))
        calls = []
        def request(path, raw=False):
            calls.append(path)
            if "artifacts?" in path:
                return {"artifacts": [{"id": 9, "name": "jev-ci-plan-7", "expired": False, "workflow_run": {"id": 8}}]}
            if path.endswith("/zip"): return buffer.getvalue()
            return publisher
        self.assertIsNotNone(jev_plan.lookup("org/deck", 7, "pull_request", "b"*40, "a"*40, request))
        for name, value in {"event": "pull_request", "path": ".github/workflows/ci-fast.yaml",
                            "head_branch": "tasks/attacker", "status": "in_progress", "conclusion": "failure",
                            "repository": {"full_name": "other/deck"}, "head_repository": {"full_name": "fork/deck"}}.items():
            original = copy.deepcopy(publisher); publisher[name] = value; calls.clear()
            self.assertIsNone(jev_plan.lookup("org/deck", 7, "pull_request", "b"*40, "a"*40, request))
            self.assertFalse(any(p.endswith("/zip") for p in calls))
            publisher.clear(); publisher.update(original)

    def test_missing_plan_times_out_to_fallback(self):
        with patch.dict(os.environ, GH_TOKEN="synthetic"), patch.object(jev_plan, "lookup", return_value=None), patch.object(jev_plan.time, "monotonic", side_effect=[0, 61]):
            self.assertIsNone(jev_plan.load("org/deck", 7, "pull_request", "b"*40, "a"*40))

    def test_default_selector_uses_exact_run_plan(self):
        event = {"repository": {"private": True}, "merge_group": {"base_sha": "b"*40, "head_sha": "a"*40}}
        baseline = {"mode": "selective", "jobs": list(ci_scope.SUITES), "path_count": 19, "reason": "fallback"}
        with patch.object(ci_scope, "plan", return_value=baseline), patch.dict(os.environ, GITHUB_RUN_ID="7", GITHUB_REPOSITORY="org/deck"), patch.object(jev_plan, "load", return_value=self.validate(fixture())) as load:
            selected = ci_scope.select_plan("merge_group", event)
            self.assertEqual(selected["jobs"], ["documentation", "frontend", "probes"])
            load.assert_called_once_with("org/deck", "7", "merge_group", "b"*40, "a"*40)

    def test_private_pr_is_preflight_without_diff_or_jev(self):
        repo = {"full_name": "org/deck"}
        event = {"repository": {"private": True}, "pull_request": {
            "base": {"repo": repo}, "head": {"repo": repo}}}
        with patch.object(ci_scope, "plan") as plan, patch.object(jev_plan, "load") as load:
            result = ci_scope.select_plan("pull_request", event)
            self.assertEqual(result["selector"], "pr-preflight")
            self.assertEqual(result["jobs"], ["documentation"])
            self.assertEqual(result["mode"], "selective")
            plan.assert_not_called()
            load.assert_not_called()

    def test_preflight_never_applies_to_public_fork_or_malformed_events(self):
        for private, base, head in [(False, "org/deck", "org/deck"),
                                    (True, "org/deck", "fork/deck"),
                                    (True, None, None)]:
            event = {"repository": {"private": private}, "pull_request": {
                "base": {"repo": {"full_name": base}},
                "head": {"repo": {"full_name": head}}}}
            baseline = ci_scope.full("existing validation")
            with self.subTest(private=private, head=head), patch.object(ci_scope, "plan", return_value=baseline):
                self.assertEqual(ci_scope.select_plan("pull_request", event), baseline)

    def test_queue_without_jev_still_runs_python_selected_tests(self):
        event = {"repository": {"private": True}, "merge_group": {"base_sha": "b"*40, "head_sha": "a"*40}}
        baseline = ci_scope.classify(["internal/history/launch.go"])
        baseline["path_count"] = 1
        with patch.object(ci_scope, "plan", return_value=baseline), patch.dict(os.environ, GITHUB_RUN_ID="7", GITHUB_REPOSITORY="org/deck"), patch.object(jev_plan, "load", return_value=None):
            result = ci_scope.select_plan("merge_group", event)
            self.assertEqual(result["selector"], "python-fallback")
            self.assertIn("go-parallel", result["jobs"])

    def test_public_manual_and_forks_do_not_consume_jev(self):
        for event_name, event, baseline in [
            ("workflow_dispatch", {}, ci_scope.full("manual")),
            ("pull_request", {"repository": {"private": False}}, {"path_count": 1}),
            ("pull_request", {"repository": {"private": True}}, ci_scope.full("fork")),
        ]:
            with patch.object(ci_scope, "plan", return_value=baseline), patch.dict(os.environ, GITHUB_RUN_ID="7", GITHUB_REPOSITORY="org/deck"), patch.object(jev_plan, "load") as load:
                self.assertEqual(ci_scope.select_plan(event_name, event), baseline)
                load.assert_not_called()


if __name__ == "__main__":
    unittest.main()
