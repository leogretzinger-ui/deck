import contextlib
import io
import json
import os
from pathlib import Path
import runpy
import shlex
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).parent / "provider-hook"
HELPER = SCRIPTS / "noisedeck-hook-diagnostics"
RECEIVER = SCRIPTS / "noisedeck-provider-hook"
ADAPTER = SCRIPTS / "noisedeck-codex-identity-hook"
STAGE_HELPER = "/usr/local/bin/noisedeck-hook-diagnostics"
SENTINEL = "SECRET_SENTINEL_DO_NOT_RECORD"


class HookDiagnosticsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="deck-hook-diagnostics-")
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.directory.chmod(0o700)
        self.log = self.directory / "hook-diagnostics.jsonl"
        self.env = patch.dict(os.environ, {"TMPDIR": str(self.directory),
                                         "NOISEDECK_HOOK_DIAGNOSTICS": "true"})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.module = runpy.run_path(str(HELPER))
        self.record = self.module["record"]

    def entries(self):
        if not self.log.exists():
            return []
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_disabled_and_invalid_values_create_no_file(self):
        for flag in ("", "false", "TRUE", "1"):
            with patch.dict(os.environ, {"NOISEDECK_HOOK_DIAGNOSTICS": flag}):
                self.record("adapter_entered")
            self.assertFalse(self.log.exists())
        self.record(SENTINEL)
        self.record("receiver_attempt", 4, 0, 204)
        self.record("receiver_attempt", 1, 0, 999)
        self.assertFalse(self.log.exists())

    def test_private_permissions_byte_cap_and_content_whitelist(self):
        for _ in range(1000):
            self.record("receiver_attempt", 1, 7, 0)
        self.assertLessEqual(self.log.stat().st_size, self.module["MAX_BYTES"])
        self.assertGreater(self.log.stat().st_size, 0)
        self.assertEqual(self.log.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.log.stat().st_uid, os.geteuid())
        self.assertNotIn(SENTINEL, self.log.read_text())
        for entry in self.entries():
            self.assertEqual(set(entry), {"stage", "attempt", "curl_exit", "http_status"})

    def test_unsafe_paths_permissions_links_and_write_errors_fail_silently(self):
        self.directory.chmod(0o755)
        self.record("adapter_entered")
        self.assertFalse(self.log.exists())
        self.directory.chmod(0o700)
        target = self.directory / "untouched"
        target.write_text(SENTINEL)
        self.log.symlink_to(target)
        self.record("adapter_entered")
        self.assertEqual(target.read_text(), SENTINEL)
        self.log.unlink()
        self.log.write_text("")
        self.log.chmod(0o644)
        self.record("adapter_entered")
        self.assertEqual(self.log.stat().st_size, 0)
        self.log.chmod(0o600)
        os.link(self.log, self.directory / "hardlink")
        self.record("adapter_entered")
        self.assertEqual(self.log.stat().st_size, 0)
        self.log.unlink()
        os.mkfifo(self.log)
        self.record("adapter_entered")
        self.log.unlink()
        with patch("os.open", side_effect=PermissionError(SENTINEL)):
            self.record("adapter_entered")
        self.assertFalse(self.log.exists())
        with patch("os.geteuid", return_value=os.geteuid() + 1):
            self.record("adapter_entered")
        self.assertFalse(self.log.exists())
        link = self.directory / "directory-link"
        link.symlink_to(self.directory, target_is_directory=True)
        with patch.dict(os.environ, {"TMPDIR": str(link)}):
            self.record("adapter_entered")
        self.assertFalse(self.log.exists())

    def test_nonblocking_lock_and_descriptors_close(self):
        import fcntl
        self.record("adapter_entered")
        before = self.log.stat().st_size
        with self.log.open("a") as locked:
            fcntl.flock(locked, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.record("adapter_entered")
        self.assertEqual(self.log.stat().st_size, before)
        descriptors = len(list(Path("/proc/self/fd").iterdir()))
        for _ in range(100):
            self.record("adapter_entered")
        self.assertEqual(len(list(Path("/proc/self/fd").iterdir())), descriptors)

    def adapter(self, body, forward_error=None):
        forwarded = []

        def run(argv, **kwargs):
            if argv[0] == STAGE_HELPER:
                self.record(argv[1])
            else:
                self.assertEqual(argv, ["/usr/local/bin/noisedeck-provider-hook"])
                if forward_error:
                    raise forward_error
                forwarded.append(json.loads(kwargs["input"]))
            return subprocess.CompletedProcess(argv, 0)

        stdout, stderr = io.StringIO(), io.StringIO()
        with patch("sys.stdin", io.StringIO(body)), patch("subprocess.run", side_effect=run), \
                contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            try:
                runpy.run_path(str(ADAPTER), run_name="__main__")
            except SystemExit as exit:
                self.assertEqual(exit.code, 0)
        self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")
        if self.log.exists():
            self.assertNotIn(SENTINEL, self.log.read_text())
        return forwarded

    def test_adapter_disabled_and_unavailable_diagnostics_do_not_block_forwarding(self):
        body = json.dumps({"hook_event_name": "SessionStart", "session_id": SENTINEL,
                           "cwd": SENTINEL, "source": "startup"})
        with patch.dict(os.environ, {"NOISEDECK_HOOK_DIAGNOSTICS": "false"}):
            self.assertEqual(len(self.adapter(body)), 1)
        self.assertFalse(self.log.exists())
        calls = []

        def unavailable(argv, **kwargs):
            if argv[0] == STAGE_HELPER:
                raise OSError(SENTINEL)
            calls.append(kwargs["input"])
            return subprocess.CompletedProcess(argv, 0)

        stdout, stderr = io.StringIO(), io.StringIO()
        with patch("sys.stdin", io.StringIO(body)), patch("subprocess.run", side_effect=unavailable), \
                contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            runpy.run_path(str(ADAPTER), run_name="__main__")
        self.assertEqual(len(calls), 1)
        self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")
        self.assertFalse(self.log.exists())

    def test_adapter_rejections_and_suppressed_errors_have_only_fixed_stages(self):
        cases = [(SENTINEL, "adapter_invalid_json"), ("[]", "adapter_invalid_shape"),
                 (json.dumps({"hook_event_name": SENTINEL}), "adapter_unsupported_event"),
                 (json.dumps({"hook_event_name": "Stop"}), "adapter_missing_session_id")]
        for body, category in cases:
            self.assertEqual(self.adapter(body), [])
            self.assertEqual(self.entries()[-1], {"stage": category})
        body = json.dumps({"hook_event_name": "Stop", "session_id": SENTINEL,
                           "cwd": SENTINEL, "transcript_path": SENTINEL})
        forwarded = self.adapter(body)
        self.assertEqual(forwarded[0], {"hook_event_name": "Stop", "session_id": SENTINEL,
                                       "cwd": SENTINEL, "source": "current"})
        self.assertEqual(self.entries()[-1], {"stage": "adapter_forwarded"})
        for failure, category in [(OSError(SENTINEL), "adapter_forward_unavailable"),
                                  (subprocess.TimeoutExpired(SENTINEL, 10), "adapter_forward_timeout")]:
            self.adapter(body, failure)
            self.assertEqual(self.entries()[-1], {"stage": category})
        for entry in self.entries():
            self.assertEqual(set(entry), {"stage"})

    def receiver(self, codes, flag="true"):
        bindir = self.directory / "bin"
        bindir.mkdir(exist_ok=True)
        count = self.directory / "attempt-count"
        count.write_text("0")
        curl = bindir / "curl"
        curl.write_text("#!/usr/bin/python3\nfrom pathlib import Path\n"
                        f"p=Path({str(count)!r}); n=int(p.read_text()); p.write_text(str(n+1))\n"
                        f"codes={codes!r}; status,code=codes[min(n,len(codes)-1)]\n"
                        "print(status,end=''); raise SystemExit(code)\n")
        curl.chmod(0o700)
        receiver = self.directory / "receiver"
        receiver.write_text(RECEIVER.read_text().replace(
            STAGE_HELPER, shlex.join([sys.executable, str(HELPER.resolve())])))
        env = dict(os.environ, PATH=str(bindir) + ":" + os.environ["PATH"],
                   NOISEDECK_HOOK_DIAGNOSTICS=flag, NOISEDECK_SESSION_HOOK_TOKEN=SENTINEL,
                   NOISEDECK_SESSION_HOOK_URL="http://" + SENTINEL, NOISEDECK_SESSION_HOOK_SOCKET="")
        result = subprocess.run(["/bin/sh", str(receiver)], input=SENTINEL,
                                text=True, capture_output=True, env=env, timeout=12)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout + result.stderr, "")
        if self.log.exists():
            self.assertNotIn(SENTINEL, self.log.read_text())
        return int(count.read_text())

    def test_receiver_delivery_codes_and_retry_exit_are_unchanged(self):
        for codes, expected, final in [([(204, 0)], 1, "receiver_accepted"),
                                      ([(409, 0), (204, 0)], 1, "receiver_client_refused"),
                                      ([(503, 0), (204, 0)], 2, "receiver_accepted"),
                                      ([(0, 7)], 3, "receiver_retry_exhausted")]:
            if self.log.exists():
                self.log.unlink()
            self.assertEqual(self.receiver(codes), expected)
            self.assertEqual(self.entries()[-1], {"stage": final})
            attempts = [e for e in self.entries() if e["stage"] == "receiver_attempt"]
            self.assertEqual(len(attempts), expected)
            self.assertEqual(attempts[0]["http_status"], codes[0][0])
            self.assertEqual(attempts[0]["curl_exit"], codes[0][1])
        self.log.unlink()
        self.assertEqual(self.receiver([(204, 0)], "false"), 1)
        self.assertFalse(self.log.exists())
        self.directory.chmod(0o500)
        try:
            self.assertEqual(self.receiver([(204, 0)]), 1)
            self.assertFalse(self.log.exists())
        finally:
            self.directory.chmod(0o700)


if __name__ == "__main__":
    unittest.main()
