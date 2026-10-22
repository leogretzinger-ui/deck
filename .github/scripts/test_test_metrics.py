import contextlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
import zipfile
from unittest.mock import patch

import test_metrics as metrics


class MetricsTests(unittest.TestCase):
    def record(self, run="1", attempt="1", outcome="fail", recorded="2026-10-21T01:00:00+00:00"):
        return dict(version=1, repository="example/deck", run=run, attempt=attempt,
                    suite="serialized", sha="abc", platform="Linux/arm64", recorded=recorded,
                    exit_code=1 if outcome == "fail" else 0,
                    results=[dict(package="example/tasks", test="TestBoundary", outcome=outcome, seconds=1.5)])

    def test_import_idempotent_and_atomic(self):
        with metrics.connect(":memory:") as db:
            record = self.record()
            metrics.ingest(db, record)
            metrics.ingest(db, record)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM results").fetchone()[0], 1)
            record["results"].append(dict(package="x", test="bad", outcome="pass", seconds=-1))
            with self.assertRaises(ValueError):
                metrics.ingest(db, record)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM results").fetchone()[0], 1)

    def test_report_recovers_only_same_commit_suite_platform(self):
        with metrics.connect(":memory:") as db:
            metrics.ingest(db, self.record())
            for key, value in (("sha", "other"), ("suite", "parallel"), ("platform", "Darwin/arm64")):
                record = self.record(run=key, outcome="pass", recorded="2026-10-21T02:00:00+00:00")
                record[key] = value
                metrics.ingest(db, record)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                metrics.report(db, 100000, 20)
            self.assertIn("| 2 | 3.00 | 1 | 0 | 0 | 1 |", output.getvalue())
            metrics.ingest(db, self.record(attempt="2", outcome="pass", recorded="2026-10-21T03:00:00+00:00"))
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                metrics.report(db, 100000, 20)
            self.assertIn("| 3 | 4.50 | 1 | 0 | 1 | 1 |", output.getvalue())

    def test_no_transcript_persisted_and_exit_preserved(self):
        events = [dict(Action="output", Output="sensitive fixture output\n"),
                  dict(Action="fail", Package="example/tasks", Test="TestBoundary", Elapsed=0.1)]
        class Process:
            stdout = [json.dumps(e) + "\n" for e in events]
            def wait(self): return 7
            def poll(self): return 7
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {"GITHUB_SHA": "abc"}), \
                patch.object(metrics.subprocess, "Popen", return_value=Process()) as launch, \
                patch.object(metrics.subprocess, "check_output", return_value="abc"), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            path = Path(directory) / "result.json"
            self.assertEqual(metrics.collect(["go", "test", "./..."], "test", path), 7)
            launch.assert_called_once_with(["go", "test", "-json", "./..."], stdout=metrics.subprocess.PIPE, text=True)
            self.assertIn("sensitive fixture output", output.getvalue())
            self.assertNotIn("sensitive", path.read_text())
            self.assertEqual(json.loads(path.read_text())["exit_code"], 7)

    def test_evidence_deduplicates(self):
        with metrics.connect(":memory:") as db:
            for _ in range(2):
                db.execute("INSERT OR IGNORE INTO evidence VALUES (?,?,?,?,?)",
                           ("example/deck", "p", "TestX", "bug", "https://example/fix"))
            self.assertEqual(db.execute("SELECT COUNT(*) FROM evidence").fetchone()[0], 1)

    def test_sync_imports_once_without_extracting_paths(self):
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as zipped:
            zipped.writestr("../../result.json", json.dumps(self.record()))
        def download(command, stdout, check):
            stdout.write(archive.getvalue())
        listing = [[dict(artifacts=[dict(id=3, name="test-metrics-serialized-1",
                                       expired=False, workflow_run=dict(id=1),
                                       size_in_bytes=len(archive.getvalue()))])]]
        with metrics.connect(":memory:") as db, \
                patch.object(metrics.subprocess, "check_output", return_value=json.dumps(listing[0])), \
                patch.object(metrics.subprocess, "run", side_effect=download) as fetch, \
                contextlib.redirect_stdout(io.StringIO()):
            metrics.sync(db, "example/deck")
            metrics.sync(db, "example/deck")
            self.assertEqual(fetch.call_count, 1)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM results").fetchone()[0], 1)

    def test_rejected_artifact_does_not_block_later_import(self):
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as zipped:
            zipped.writestr("result.json", json.dumps(self.record()))
        listing = [dict(artifacts=[
            dict(id=5, name="test-metrics-bad", expired=False, size_in_bytes=30_000_000),
            dict(id=6, name="test-metrics-good", expired=False, size_in_bytes=100, workflow_run=dict(id=1))])]
        def download(command, stdout, check):
            stdout.write(archive.getvalue())
        with metrics.connect(":memory:") as db, \
                patch.object(metrics.subprocess, "check_output", return_value=json.dumps(listing)), \
                patch.object(metrics.subprocess, "run", side_effect=download) as fetch, \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(metrics.sync(db, "example/deck"), 1)
            self.assertEqual(metrics.sync(db, "example/deck"), 0)
            self.assertEqual(fetch.call_count, 1)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM results").fetchone()[0], 1)

    def test_success_logs_are_quiet_and_metrics_write_failure_is_nonblocking(self):
        events = [dict(Action="output", Package="p", Output="=== RUN TestX\nsecret fixture\n"),
                  dict(Action="pass", Package="p", Test="TestX", Elapsed=0.1),
                  dict(Action="output", Package="p", Output="ok  \tp\t0.1s\n"),
                  dict(Action="pass", Package="p", Elapsed=0.1)]
        class Process:
            stdout = [json.dumps(e) + "\n" for e in events]
            def wait(self): return 0
            def poll(self): return 0
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(metrics.subprocess, "Popen", return_value=Process()), \
                patch.dict(os.environ, {"GITHUB_SHA": ""}), \
                patch.object(metrics.subprocess, "check_output", side_effect=OSError("no git")), \
                contextlib.redirect_stdout(io.StringIO()) as output, \
                contextlib.redirect_stderr(io.StringIO()) as errors:
            blocker = Path(directory) / "file"
            blocker.write_text("not a directory")
            self.assertEqual(metrics.collect(["go", "test", "./..."], "test", blocker / "x.json"), 0)
            self.assertEqual(output.getvalue(), "ok  \tp\t0.1s\n")
            self.assertIn("metrics not saved", errors.getvalue())

    def test_compiler_diagnostics_reach_console(self):
        class Process:
            stdout = [json.dumps(dict(Action="build-output", ImportPath="p", Output="file.go:3: type error\n"))]
            def wait(self): return 1
            def poll(self): return 1
        with tempfile.TemporaryDirectory() as directory, \
                patch.dict(os.environ, {"GITHUB_SHA": "abc"}), \
                patch.object(metrics.subprocess, "Popen", return_value=Process()), \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(metrics.collect(["go", "test", "./..."], "test", Path(directory) / "r.json"), 1)
            self.assertEqual(output.getvalue(), "file.go:3: type error\n")

    def test_malformed_results_and_timezone_normalization(self):
        with metrics.connect(":memory:") as db:
            for bad in (None, [None], ["bad"]):
                record = self.record()
                record["results"] = bad
                with self.assertRaises(ValueError):
                    metrics.ingest(db, record)
            record = self.record(recorded="2026-10-21T03:00:00+02:00")
            metrics.ingest(db, record)
            self.assertEqual(db.execute("SELECT recorded FROM runs").fetchone()[0],
                             "2026-10-21T01:00:00.000000+00:00")

    def test_unrelated_failure_is_not_unique(self):
        with metrics.connect(":memory:") as db:
            record = self.record()
            record["results"].append(dict(package="example/tasks", test="TestOther", outcome="fail", seconds=1))
            metrics.ingest(db, record)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                metrics.report(db, 100000, 20)
            self.assertIn("| 1 | 1.50 | 1 | 0 | 0 | 0 |", output.getvalue())

    def test_same_test_name_in_different_package_is_not_unique(self):
        with metrics.connect(":memory:") as db:
            record = self.record()
            record["results"].append(dict(package="example/other", test="TestBoundary", outcome="fail", seconds=1))
            metrics.ingest(db, record)
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                metrics.report(db, 100000, 20)
            self.assertIn("| 1 | 1.50 | 1 | 0 | 0 | 0 |", output.getvalue())


if __name__ == "__main__":
    unittest.main()
