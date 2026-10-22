from __future__ import annotations

import concurrent.futures
import http.server
import json
import os
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
from unittest import mock

import hook_forwarder


HERE = Path(__file__).resolve().parent
FORWARDER = HERE / "hook_forwarder.py"
CONFIGURE = HERE / "configure.py"
HOOKS = HERE / "hooks"


class MCPServer:
    def __init__(self, responses: list[tuple[int, dict]] | None = None):
        self.requests: list[dict] = []
        self.responses = list(responses or [])
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                owner.requests.append(json.loads(self.rfile.read(length)))
                status, payload = (
                    owner.responses.pop(0)
                    if owner.responses
                    else (
                        200,
                        {
                            "jsonrpc": "2.0",
                            "id": owner.requests[-1]["id"],
                            "result": {
                                "content": [
                                    {
                                        "type": "text",
                                        "text": json.dumps(
                                            {
                                                "success": True,
                                                "drawer_id": "drawer-test",
                                            }
                                        ),
                                    }
                                ]
                            },
                        },
                    )
                )
                body = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                return

        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_port}/mcp"

    def __enter__(self):
        self.thread.start()
        return self

    def __exit__(self, *args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


class HookForwarderTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.home = self.root / "home"
        (self.home / ".mempalace").mkdir(parents=True)
        self.outbox = self.home / ".mempalace" / "hook_outbox"
        self.env = os.environ.copy()
        self.env.update(
            {
                "HOME": str(self.home),
                "NOISEDECK_MEMPALACE_HOOK_AUTODRAIN": "0",
                "NOISEDECK_MEMPALACE_HOOK_TIMEOUT_SECONDS": "0.2",
                "NOISEDECK_MEMPALACE_TRANSCRIPT_ROOTS": str(self.root),
            }
        )

    def tearDown(self):
        self.temporary.cleanup()

    def transcript(self, name: str, count: int) -> Path:
        path = self.root / f"{name}.jsonl"
        records = [
            json.dumps(
                {
                    "cwd": f"/workspace/{name}",
                    "message": {"role": "user", "content": f"message {index} for {name}"},
                }
            )
            for index in range(count)
        ]
        path.write_text("\n".join(records) + "\n", encoding="utf-8")
        return path

    def hook(self, hook: str, session: str, transcript: Path, *, harness="claude-code"):
        result = subprocess.run(
            [
                sys.executable,
                str(FORWARDER),
                "--hook",
                hook,
                "--harness",
                harness,
            ],
            input=json.dumps(
                {
                    "session_id": session,
                    "transcript_path": str(transcript),
                    "stop_hook_active": False,
                }
            ),
            text=True,
            capture_output=True,
            env=self.env,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {})

    def drain(self, url: str):
        env = dict(self.env, NOISEDECK_MEMPALACE_MCP_URL=url)
        return subprocess.run(
            [sys.executable, str(FORWARDER), "--drain"],
            text=True,
            capture_output=True,
            env=env,
            check=False,
        )

    def ready(self) -> list[Path]:
        return sorted(self.outbox.glob("*.ready")) if self.outbox.exists() else []

    def assert_chat_instructions(self, path: Path) -> None:
        instructions = path.read_text()
        self.assertIn("use the `a2a-chat` skill", instructions)
        self.assertIn("`[a2a]` and `[a2a#...]`", instructions)
        self.assertIn("live invitations, messages, or joins", instructions)
        self.assertIn('`a2a reply <chat> "..."` to reply in place', instructions)
        self.assertIn("`a2a read <chat>` for history", instructions)
        self.assertIn("Departure and closed-chat notices need no reply", instructions)
        self.assertIn("do not open a new chat to answer them", instructions)
        self.assertNotIn("answer it with `a2a chat`", instructions)

    def test_stop_threshold_spools_once_and_advances_after_durable_write(self):
        path = self.transcript("deck", 14)
        self.hook("stop", "session-one", path)
        self.assertEqual(self.ready(), [])

        path = self.transcript("deck", 15)
        self.hook("stop", "session-one", path)
        first = self.ready()
        self.assertEqual(len(first), 1)
        marker = self.home / ".mempalace" / "hook_state" / "session-one_last_save"
        self.assertEqual(marker.read_text(), "15")

        self.hook("stop", "session-one", path)
        self.assertEqual(self.ready(), first)

        path = self.transcript("deck", 30)
        self.hook("stop", "session-one", path)
        jobs = self.ready()
        self.assertEqual(len(jobs), 2)
        payloads = [json.loads(job.read_text())["request"] for job in jobs]
        contents = [payload["params"]["arguments"]["content"] for payload in payloads]
        self.assertTrue(any("|msgs:30|" in content for content in contents))

    def test_transport_failure_replays_to_checkpoint_without_mining(self):
        path = self.transcript("deck", 2)
        self.hook("session-end", "session-two", path)
        self.assertEqual(len(self.ready()), 1)

        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        unavailable = f"http://127.0.0.1:{sock.getsockname()[1]}/mcp"
        sock.close()
        self.assertEqual(self.drain(unavailable).returncode, 0)
        self.assertEqual(len(self.ready()), 1)

        with MCPServer() as server:
            self.assertEqual(self.drain(server.url).returncode, 0)
            self.assertEqual(len(server.requests), 1)
            request = server.requests[0]
        self.assertEqual(self.ready(), [])
        self.assertEqual(request["params"]["name"], "mempalace_add_drawer")
        arguments = request["params"]["arguments"]
        self.assertEqual(arguments["room"], "session_checkpoints")

        source = FORWARDER.read_text(encoding="utf-8")
        self.assertNotIn('"mempalace_mine"', source)
        self.assertNotIn("mempalace hook run", source)

    def test_ambiguous_retry_reuses_the_same_idempotent_request(self):
        path = self.transcript("deck", 2)
        self.hook("precompact", "session-three", path)
        failures = [
            (500, {"error": "temporary"}),
            (500, {"error": "temporary"}),
        ]
        with MCPServer(failures) as server:
            self.drain(server.url)
            self.assertEqual(len(server.requests), 2)
            self.assertEqual(server.requests[0], server.requests[1])
            original = server.requests[0]
        self.assertEqual(len(self.ready()), 1)

        with MCPServer() as server:
            self.drain(server.url)
            replay = server.requests[0]
        self.assertEqual(self.ready(), [])
        self.assertEqual(replay, original)
        self.assertEqual(replay["params"]["name"], "mempalace_add_drawer")

    def test_concurrent_producers_publish_only_complete_jobs(self):
        def produce(index: int):
            path = self.transcript(f"project-{index}", 2)
            self.hook("session-end", f"session-{index}", path)

        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(produce, range(20)))

        jobs = self.ready()
        self.assertEqual(len(jobs), 20)
        self.assertEqual(list(self.outbox.glob("*.tmp")), [])
        for path in jobs:
            document = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(document["version"], 1)
            self.assertEqual(document["request"]["method"], "tools/call")

    def test_concurrent_stops_for_one_session_spool_one_checkpoint(self):
        path = self.transcript("shared", 15)

        def produce(_index: int):
            self.hook("stop", "same-session", path)

        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(produce, range(20)))

        self.assertEqual(len(self.ready()), 1)
        marker = self.home / ".mempalace" / "hook_state" / "same-session_last_save"
        self.assertEqual(marker.read_text(), "15")

    def test_shrinking_transcript_resets_marker_and_recovers(self):
        path = self.transcript("rotated", 30)
        self.hook("stop", "rotated-session", path)
        self.assertEqual(len(self.ready()), 1)

        path = self.transcript("rotated", 15)
        self.hook("stop", "rotated-session", path)
        self.assertEqual(len(self.ready()), 2)
        log = (self.home / ".mempalace" / "hook_state" / "forwarder.log").read_text()
        self.assertIn("event=marker_reset", log)

    def test_hook_boundary_returns_success_after_unexpected_enqueue_error(self):
        output = StringIO()
        with (
            mock.patch.object(hook_forwarder, "_enqueue_hook", side_effect=ValueError),
            mock.patch.object(hook_forwarder, "_start_drainer"),
            mock.patch.object(sys, "stdin", StringIO("{}")),
            mock.patch.dict(os.environ, {"HOME": str(self.home)}),
            redirect_stdout(output),
        ):
            hook_forwarder._hook_main("stop", "codex")
        self.assertEqual(json.loads(output.getvalue()), {})
        log = (self.home / ".mempalace" / "hook_state" / "forwarder.log").read_text()
        self.assertIn("event=queue_failed", log)

    def test_transcript_path_must_be_under_the_provider_root(self):
        outside = self.root.parent / f"{self.root.name}-outside.jsonl"
        outside.write_text(
            json.dumps({"message": {"role": "user", "content": "do not capture"}}),
            encoding="utf-8",
        )
        try:
            self.hook("session-end", "outside-session", outside)
            self.assertEqual(self.ready(), [])
            log = (self.home / ".mempalace" / "hook_state" / "forwarder.log").read_text()
            self.assertIn("event=invalid_transcript", log)
        finally:
            outside.unlink(missing_ok=True)

    def test_codex_event_transcript_spools_a_codex_checkpoint(self):
        path = self.root / "codex.jsonl"
        path.write_text(
            json.dumps(
                {
                    "cwd": "/workspace/deck",
                    "type": "event_msg",
                    "payload": {"type": "user_message", "message": "codex turn"},
                }
            )
            + "\n",
            encoding="utf-8",
        )
        self.hook("precompact", "codex-session", path, harness="codex")
        request = json.loads(self.ready()[0].read_text())["request"]
        self.assertEqual(request["params"]["arguments"]["added_by"], "codex")
        self.assertIn("codex turn", request["params"]["arguments"]["content"])

    def test_pi_family_transcripts_spool_bounded_checkpoints(self):
        for harness in ("pi", "omp"):
            with self.subTest(harness=harness):
                path = self.root / f"{harness}.jsonl"
                path.write_text(
                    "\n".join(
                        (
                            json.dumps(
                                {
                                    "type": "session",
                                    "id": f"{harness}-session",
                                    "cwd": "/workspace/deck",
                                }
                            ),
                            json.dumps(
                                {
                                    "type": "message",
                                    "message": {
                                        "role": "user",
                                        "content": f"{harness} user turn",
                                    },
                                }
                            ),
                        )
                    )
                    + "\n",
                    encoding="utf-8",
                )
                self.hook(
                    "precompact",
                    f"{harness}-session",
                    path,
                    harness=harness,
                )
                requests = [
                    json.loads(job.read_text())["request"] for job in self.ready()
                ]
                matching = [
                    request
                    for request in requests
                    if request["params"]["arguments"]["added_by"] == harness
                ]
                self.assertEqual(len(matching), 1)
                arguments = matching[0]["params"]["arguments"]
                self.assertIn(f"{harness} user turn", arguments["content"])
                self.assertNotIn("assistant", arguments["content"])

    def test_mcp_override_is_restricted_to_loopback(self):
        request = {
            "jsonrpc": "2.0",
            "id": "test",
            "method": "tools/call",
            "params": {},
        }
        with mock.patch.dict(
            os.environ,
            {"NOISEDECK_MEMPALACE_MCP_URL": "https://example.com/mcp"},
        ):
            self.assertEqual(
                hook_forwarder._deliver(request),
                ("permanent", "invalid_mcp_url"),
            )

    def test_non_json_http_response_is_permanent(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                body = b"not json"
                self.send_response(200)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                return

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            request = {
                "jsonrpc": "2.0",
                "id": "test",
                "method": "tools/call",
                "params": {},
            }
            with mock.patch.dict(
                os.environ,
                {
                    "NOISEDECK_MEMPALACE_MCP_URL": (
                        f"http://127.0.0.1:{server.server_port}/mcp"
                    )
                },
            ):
                self.assertEqual(
                    hook_forwarder._deliver(request),
                    ("permanent", "invalid_response"),
                )
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_persistent_request_failure_does_not_starve_later_job(self):
        self.hook("session-end", "first", self.transcript("first", 2))
        self.hook("session-end", "second", self.transcript("second", 2))
        jobs = self.ready()
        self.assertEqual(len(jobs), 2)
        failing_id = json.loads(jobs[0].read_text())["request"]["id"]

        class SelectiveServer(MCPServer):
            def __init__(self):
                super().__init__()
                owner = self

                class Handler(http.server.BaseHTTPRequestHandler):
                    def do_POST(self):
                        length = int(self.headers.get("Content-Length", "0"))
                        request = json.loads(self.rfile.read(length))
                        owner.requests.append(request)
                        if request["id"] == failing_id:
                            status = 500
                            payload = {"error": "request-specific"}
                        else:
                            status = 200
                            payload = {
                                "jsonrpc": "2.0",
                                "id": request["id"],
                                "result": {
                                    "content": [
                                        {
                                            "type": "text",
                                            "text": json.dumps({"success": True}),
                                        }
                                    ]
                                },
                            }
                        body = json.dumps(payload).encode()
                        self.send_response(status)
                        self.send_header("Content-Length", str(len(body)))
                        self.end_headers()
                        self.wfile.write(body)

                    def log_message(self, *args):
                        return

                self.server.server_close()
                self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

        with SelectiveServer() as server:
            self.drain(server.url)
        remaining = self.ready()
        self.assertEqual(len(remaining), 1)
        self.assertEqual(json.loads(remaining[0].read_text())["request"]["id"], failing_id)

    def test_transient_jobs_rotate_beyond_the_drain_window(self):
        for index in range(17):
            self.hook(
                "session-end",
                f"rotation-{index}",
                self.transcript(f"rotation-{index}", 2),
            )
        original = hook_forwarder._ready_jobs(self.outbox, 17)
        first_window_ids = {
            json.loads(path.read_text())["request"]["id"] for path in original[:16]
        }
        final_id = json.loads(original[16].read_text())["request"]["id"]

        failures = [(500, {"error": "temporary"}) for _ in range(32)]
        with MCPServer(failures) as server:
            self.drain(server.url)
        self.assertEqual(
            {request["id"] for request in server.requests},
            first_window_ids,
        )

        with MCPServer() as server:
            self.drain(server.url)
            delivered = {request["id"] for request in server.requests}
        self.assertIn(final_id, delivered)

    def test_request_specific_failure_is_preserved_after_retry_budget(self):
        self.hook("session-end", "poison", self.transcript("poison", 2))
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_HOOK_MAX_ATTEMPTS="2",
        )
        failures = [(500, {"error": "request-specific"}) for _ in range(4)]
        with MCPServer(failures) as server:
            for _ in range(2):
                result = subprocess.run(
                    [sys.executable, str(FORWARDER), "--drain"],
                    text=True,
                    capture_output=True,
                    env=dict(env, NOISEDECK_MEMPALACE_MCP_URL=server.url),
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.ready(), [])
        failed = list(self.outbox.glob("*.failed"))
        self.assertEqual(len(failed), 1)
        request = json.loads(failed[0].read_text())["request"]
        self.assertEqual(request["params"]["arguments"]["room"], "session_checkpoints")
        log = (self.home / ".mempalace" / "hook_state" / "forwarder.log").read_text()
        self.assertIn("event=retry_exhausted", log)

    def test_writer_outage_never_consumes_request_retry_budget(self):
        self.hook("session-end", "busy", self.transcript("busy", 2))
        response = {
            "jsonrpc": "2.0",
            "id": "ignored",
            "error": {"code": -32001, "message": "peer writer active"},
        }
        with MCPServer([(200, response) for _ in range(6)]) as server:
            for _ in range(3):
                env = dict(
                    self.env,
                    NOISEDECK_MEMPALACE_MCP_URL=server.url,
                    NOISEDECK_MEMPALACE_HOOK_MAX_ATTEMPTS="1",
                )
                result = subprocess.run(
                    [sys.executable, str(FORWARDER), "--drain"],
                    text=True,
                    capture_output=True,
                    env=env,
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
        ready = self.ready()
        self.assertEqual(len(ready), 1)
        envelope = json.loads(ready[0].read_text())
        self.assertNotIn("attempts", envelope)

    def test_malformed_and_permanent_jobs_are_dead_lettered(self):
        self.outbox.mkdir(parents=True)
        malformed = self.outbox / "bad.ready"
        malformed.write_text("not json", encoding="utf-8")
        with MCPServer() as server:
            self.drain(server.url)
        self.assertFalse(malformed.exists())
        self.assertTrue((self.outbox / "bad.failed").exists())

        path = self.transcript("deck", 2)
        self.hook("session-end", "session-four", path)
        response = {
            "jsonrpc": "2.0",
            "id": "ignored",
            "error": {"code": -32602, "message": "bad params"},
        }
        with MCPServer([(200, response)]) as server:
            self.drain(server.url)
        self.assertEqual(self.ready(), [])
        self.assertEqual(len(list(self.outbox.glob("*.failed"))), 2)

    def test_peer_contention_is_reported_as_writer_busy_and_retried_later(self):
        path = self.transcript("deck", 2)
        self.hook("session-end", "session-five", path)
        response = {
            "jsonrpc": "2.0",
            "id": "ignored",
            "error": {"code": -32001, "message": "peer writer active"},
        }
        with MCPServer([(200, response), (200, response)]) as server:
            self.drain(server.url)
        self.assertEqual(len(self.ready()), 1)
        log = (self.home / ".mempalace" / "hook_state" / "forwarder.log").read_text()
        self.assertIn("event=writer_busy", log)
        self.assertNotIn("read-only", log.lower())

    def test_configure_preserves_trusted_hook_commands_backed_by_forwarder(self):
        skills = self.root / "skills"
        for name in ("mempalace", "mempalace-recall", "mempalace-logstream", "a2a-chat"):
            target = skills / name
            target.mkdir(parents=True)
            (target / "SKILL.md").write_text(name, encoding="utf-8")
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_SKILLS_PATH=str(skills),
            NOISEDECK_MEMPALACE_PI_HOOK_PATH=str(HOOKS / "mempal-pi-hook.js"),
            NOISEDECK_A2A_PI_HOOK_PATH=str(HOOKS / "a2a-pi-hook.js"),
            NOISEDECK_A2A_OPENCODE_HOOK_PATH=str(HOOKS / "a2a-opencode-hook.js"),
        )
        codex_hooks = self.home / ".codex" / "hooks.json"
        codex_hooks.parent.mkdir(parents=True)
        codex_hooks.write_text(
            json.dumps(
                {
                    "hooks": {
                        "SessionStart": [
                            {
                                "hooks": [
                                    {
                                        "type": "command",
                                        "command": "/usr/local/bin/noisedeck-provider-hook",
                                    }
                                ]
                            }
                        ]
                    }
                }
            ),
            encoding="utf-8",
        )
        opencode_path = self.home / ".config" / "opencode" / "opencode.json"
        opencode_path.parent.mkdir(parents=True)
        opencode_path.write_text(
            json.dumps({"instructions": ["/home/agent/custom-instructions.md"]}),
            encoding="utf-8",
        )
        custom_recall = (
            self.home / ".pi" / "agent" / "skills" / "mempalace-recall" / "SKILL.md"
        )
        custom_recall.parent.mkdir(parents=True)
        custom_recall.write_text("user-managed recall skill", encoding="utf-8")
        for _ in range(2):
            result = subprocess.run(
                [sys.executable, str(CONFIGURE)],
                text=True,
                capture_output=True,
                env=env,
                check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)

        claude = json.loads((self.home / ".claude" / "settings.json").read_text())
        codex = json.loads((self.home / ".codex" / "hooks.json").read_text())
        session_start = codex["hooks"]["SessionStart"]
        self.assertEqual(
            session_start[0]["hooks"][0]["command"],
            "/usr/local/bin/noisedeck-provider-hook",
        )
        self.assertEqual(
            session_start[1]["hooks"][0]["command"],
            "/opt/mempalace/hooks/mempal-hook.sh session-start",
        )
        rendered = json.dumps({"claude": claude, "codex": codex})
        self.assertIn("/opt/mempalace/hooks/mempal-hook.sh session-start", rendered)
        self.assertIn("/opt/mempalace/hooks/mempal-stop-hook.sh", rendered)
        self.assertNotIn("mempalace hook run", rendered)

        wrappers = "\n".join(path.read_text() for path in HOOKS.glob("*.sh"))
        self.assertIn("mempalace-hook-forwarder.py", wrappers)
        self.assertNotIn("mempalace hook run", wrappers)
        self.assertNotIn("mempalace mine", wrappers)

        opencode_root = self.home / ".config" / "opencode"
        opencode = json.loads((opencode_root / "opencode.json").read_text())
        self.assertEqual(
            opencode["mcp"]["mempalace"],
            {"type": "remote", "url": "http://127.0.0.1:8765/mcp"},
        )
        self.assertEqual(
            opencode["instructions"],
            [
                "/home/agent/custom-instructions.md",
                str(opencode_root / "MEMPALACE.md"),
            ],
        )
        self.assertTrue((opencode_root / "skills" / "mempalace" / "SKILL.md").is_file())
        self.assertTrue(
            (opencode_root / "skills" / "mempalace-logstream" / "SKILL.md").is_file()
        )

        self.assertTrue(
            (
                self.home / ".claude" / "skills" / "mempalace-logstream" / "SKILL.md"
            ).is_file()
        )

        self.assertTrue(
            (
                self.home / ".kimi-code" / "skills" / "mempalace-logstream" / "SKILL.md"
            ).is_file()
        )
        self.assertTrue(
            (
                self.home / ".codex" / "skills" / "mempalace-logstream" / "SKILL.md"
            ).is_file()
        )

        codex_instructions = (self.home / ".codex" / "AGENTS.md").read_text()
        self.assertIn("use the `mempalace-logstream` skill", codex_instructions)
        self.assertEqual(codex_instructions.count("mempalace-logstream"), 1)
        self.assertIn("use the `a2a-chat` skill", codex_instructions)
        for root in (
            self.home / ".claude",
            self.home / ".codex",
            self.home / ".kimi-code",
            self.home / ".config" / "opencode",
            self.home / ".pi" / "agent",
            self.home / ".omp" / "agent",
        ):
            self.assertTrue((root / "skills" / "a2a-chat" / "SKILL.md").is_file(), root)
            filename = "CLAUDE.md" if root.name == ".claude" else "MEMPALACE.md" if root.name == "opencode" else "AGENTS.md"
            self.assert_chat_instructions(root / filename)
        claude_hooks = json.loads((self.home / ".claude" / "settings.json").read_text())["hooks"]
        for event, lifecycle in (("SessionStart", "session-start"), ("UserPromptSubmit", "prompt"), ("Stop", "stop"), ("SessionEnd", "session-end")):
            commands = [hook["command"] for group in claude_hooks[event] for hook in group["hooks"]]
            # The harness name a2a records is the Deck provider id, claude:
            # the agent's handle is derived from it on both sides.
            self.assertIn(f"/opt/mempalace/hooks/a2a-hook.sh {lifecycle} claude", commands, event)
        claude_settings = json.loads((self.home / ".claude" / "settings.json").read_text())
        self.assertEqual(claude_settings.get("crossSessionInbound"), "accept",
                         "without accept the courier's socket posts wait on an approval dialog")
        self.assertTrue((self.home / ".pi" / "agent" / "extensions" / "a2a.js").is_file())

        source_hook = (HOOKS / "mempal-pi-hook.js").read_bytes()
        for root in (self.home / ".pi" / "agent", self.home / ".omp" / "agent"):
            self.assertEqual((root / "extensions" / "mempalace.js").read_bytes(), source_hook)
            self.assertTrue((root / "skills" / "mempalace" / "SKILL.md").is_file())
            self.assertTrue(
                (root / "skills" / "mempalace-logstream" / "SKILL.md").is_file()
            )
            instructions = (root / "AGENTS.md").read_text()
            self.assertEqual(instructions.count("<!-- NOISEDECK_MEMPALACE_START -->"), 1)
        self.assertEqual(custom_recall.read_text(), "user-managed recall skill")

        result = subprocess.run(
            [sys.executable, str(CONFIGURE), "--remove"],
            text=True,
            capture_output=True,
            env=env,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        codex = json.loads(codex_hooks.read_text())
        self.assertEqual(
            codex["hooks"]["SessionStart"][0]["hooks"][0]["command"],
            "/usr/local/bin/noisedeck-provider-hook",
        )
        self.assertNotIn("mempal", json.dumps(codex).lower())
        opencode = json.loads(opencode_path.read_text())
        self.assertEqual(opencode["instructions"], ["/home/agent/custom-instructions.md"])
        self.assertNotIn("mempalace", opencode.get("mcp", {}))
        self.assertFalse((self.home / ".codex" / "skills" / "mempalace").exists())
        self.assertFalse((self.home / ".pi" / "agent" / "extensions" / "mempalace.js").exists())
        self.assertFalse((self.home / ".codex" / "AGENTS.md").exists())
        claude = json.loads((self.home / ".claude" / "settings.json").read_text())
        self.assertNotIn("crossSessionInbound", claude, "removal leaves no a2a setting behind")

    def test_configure_upgrades_chat_instructions_without_changing_user_text(self):
        skills = self.root / "skills"
        for name in ("mempalace", "mempalace-recall", "mempalace-logstream", "a2a-chat"):
            target = skills / name
            target.mkdir(parents=True)
            (target / "SKILL.md").write_text(name, encoding="utf-8")
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_SKILLS_PATH=str(skills),
            NOISEDECK_MEMPALACE_PI_HOOK_PATH=str(HOOKS / "mempal-pi-hook.js"),
            NOISEDECK_A2A_PI_HOOK_PATH=str(HOOKS / "a2a-pi-hook.js"),
            NOISEDECK_A2A_OPENCODE_HOOK_PATH=str(HOOKS / "a2a-opencode-hook.js"),
        )
        paths = [self.home / relative for relative in (
            ".claude/CLAUDE.md", ".codex/AGENTS.md", ".kimi-code/AGENTS.md",
            ".config/opencode/MEMPALACE.md", ".pi/agent/AGENTS.md", ".omp/agent/AGENTS.md",
        )]
        start = "<!-- NOISEDECK_MEMPALACE_START -->"
        end = "<!-- NOISEDECK_MEMPALACE_END -->"
        prefix = "# User instructions\n\nKeep this prefix.\n\n"
        suffix = "\n\nKeep this suffix exactly.\n"
        old = 'A prompt that starts with `[a2a]` is a message from another agent; answer it with `a2a chat`.'
        for path in paths:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"{prefix}{start}\n{old}\n{end}{suffix}", encoding="utf-8")
        previous = {}
        for _ in range(2):
            result = subprocess.run(
                [sys.executable, str(CONFIGURE)], text=True, capture_output=True,
                env=env, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            for path in paths:
                with self.subTest(path=path.relative_to(self.home)):
                    self.assert_chat_instructions(path)
                    actual = path.read_text()
                    self.assertEqual(actual.count(start), 1)
                    self.assertEqual(actual.count(end), 1)
                    self.assertEqual(actual.split(start)[0], prefix)
                    self.assertEqual(actual.split(end)[1], suffix)
                    self.assertNotIn(old, actual)
                    if path == self.home / ".kimi-code" / "AGENTS.md":
                        self.assertIn("check `a2a inbox` between work phases", actual)
                        self.assertIn("before starting review, rebasing, or merging", actual)
                        self.assertIn("use `a2a wait` rather than repeatedly polling", actual)
                        self.assertIn("do not interrupt an in-flight tool call", actual)
                    else:
                        self.assertNotIn("## Active agent chats", actual)
                    if path in previous:
                        self.assertEqual(actual, previous[path], "rerunning configuration is byte-identical")
                    previous[path] = actual

    def test_released_codex_composition_matches_provisioner_upgrade_input(self):
        repo = HERE.parent.parent
        assets = repo / "cmd" / "noisedeck-provision" / "assets"
        hooks_path = self.home / ".codex" / "hooks.json"
        hooks_path.parent.mkdir(parents=True)
        hooks_path.write_bytes((assets / "codex-hooks-v1.json").read_bytes())

        skills = self.root / "skills"
        for name in ("mempalace", "mempalace-recall", "mempalace-logstream", "a2a-chat"):
            target = skills / name
            target.mkdir(parents=True)
            (target / "SKILL.md").write_text(name, encoding="utf-8")
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_SKILLS_PATH=str(skills),
            NOISEDECK_MEMPALACE_PI_HOOK_PATH=str(HOOKS / "mempal-pi-hook.js"),
            NOISEDECK_A2A_PI_HOOK_PATH=str(HOOKS / "a2a-pi-hook.js"),
            NOISEDECK_A2A_OPENCODE_HOOK_PATH=str(HOOKS / "a2a-opencode-hook.js"),
        )
        result = subprocess.run(
            [sys.executable, str(CONFIGURE)],
            text=True,
            capture_output=True,
            env=env,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            hooks_path.read_bytes(),
            (assets / "codex-hooks-mempalace-v1.json").read_bytes(),
        )

        hooks_path.write_bytes((assets / "codex-hooks.json").read_bytes())
        result = subprocess.run(
            [sys.executable, str(CONFIGURE)],
            text=True,
            capture_output=True,
            env=env,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            hooks_path.read_bytes(),
            (assets / "codex-hooks-mempalace.json").read_bytes(),
        )

        result = subprocess.run(
            [sys.executable, str(CONFIGURE), "--remove"],
            text=True,
            capture_output=True,
            env=env,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            hooks_path.read_bytes(),
            (assets / "codex-hooks.json").read_bytes(),
        )

    def test_configure_wires_the_kimi_a2a_lifecycle_hooks(self):
        skills = self.root / "skills"
        for name in ("mempalace", "mempalace-recall", "mempalace-logstream", "a2a-chat"):
            target = skills / name
            target.mkdir(parents=True)
            (target / "SKILL.md").write_text(name, encoding="utf-8")
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_SKILLS_PATH=str(skills),
            NOISEDECK_MEMPALACE_PI_HOOK_PATH=str(HOOKS / "mempal-pi-hook.js"),
            NOISEDECK_A2A_PI_HOOK_PATH=str(HOOKS / "a2a-pi-hook.js"),
            NOISEDECK_A2A_OPENCODE_HOOK_PATH=str(HOOKS / "a2a-opencode-hook.js"),
        )
        config_path = self.home / ".kimi-code" / "config.toml"
        config_path.parent.mkdir(parents=True)
        user_tables = 'default_model = "kimi-code/k3"\n\n[[hooks]]\nevent = "SessionStart"\nmatcher = "startup"\ncommand = "/usr/local/bin/noisedeck-provider-hook"\ntimeout = 30\n\n[thinking]\nenabled = true\n'
        config_path.write_text(user_tables, encoding="utf-8")
        previous = None
        for _ in range(2):
            result = subprocess.run(
                [sys.executable, str(CONFIGURE)], text=True, capture_output=True,
                env=env, check=False,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            text = config_path.read_text(encoding="utf-8")
            for event, argument in (("SessionStart", "session-start"), ("UserPromptSubmit", "prompt"), ("Stop", "stop"), ("SessionEnd", "session-end")):
                self.assertIn(f'event = "{event}"\ncommand = "/opt/mempalace/hooks/a2a-hook.sh {argument} kimi"', text)
            self.assertEqual(text.count("a2a-hook.sh"), 4, "one block per lifecycle event, rerun replaces rather than duplicates")
            self.assertIn('command = "/usr/local/bin/noisedeck-provider-hook"', text, "the user's own hook survives")
            self.assertIn("[thinking]", text)
            if previous is not None:
                self.assertEqual(text, previous, "rerunning configuration is byte-identical")
            previous = text
        result = subprocess.run(
            [sys.executable, str(CONFIGURE), "--remove"], text=True, capture_output=True,
            env=env, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        text = config_path.read_text(encoding="utf-8")
        self.assertNotIn("a2a-hook", text)
        self.assertIn('command = "/usr/local/bin/noisedeck-provider-hook"', text)
        self.assertIn("[thinking]", text)

    def test_kimi_hook_removal_preserves_indented_tables_strings_and_unrelated_hooks(self):
        skills = self.root / "skills"
        for name in ("mempalace", "mempalace-recall", "mempalace-logstream", "a2a-chat"):
            target = skills / name
            target.mkdir(parents=True)
            (target / "SKILL.md").write_text(name, encoding="utf-8")
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_SKILLS_PATH=str(skills),
            NOISEDECK_MEMPALACE_PI_HOOK_PATH=str(HOOKS / "mempal-pi-hook.js"),
            NOISEDECK_A2A_PI_HOOK_PATH=str(HOOKS / "a2a-pi-hook.js"),
            NOISEDECK_A2A_OPENCODE_HOOK_PATH=str(HOOKS / "a2a-opencode-hook.js"),
        )
        config_path = self.home / ".kimi-code" / "config.toml"
        config_path.parent.mkdir(parents=True)
        config_path.write_text(
            'default_model = "kimi-code/k3"\n'
            '\n'
            '[[hooks]]\n'
            '# watches the same events a2a-hook does, but is not ours\n'
            'event = "Stop"\n'
            'command = "/home/agent/my-own-hook.sh"\n'
            '\n'
            '# Example delimiter: """\n'
            '  [thinking]  \n'
            '  enabled = true\n'
            '\n'
            '[notes]\n'
            'sample = \'a """ and a [[hooks]] inside a literal string\'\n'
            'escaped = """\n'
            'backslash-escaped delimiter: \\"""\n'
            'still the same string\n'
            '"""\n'
            'run = """value""""\n'
            'text = """\n'
            '[[hooks]]\n'
            'event = "Stop"\n'
            'a header-looking line inside a multiline string\n'
            '"""\n',
            encoding="utf-8",
        )
        result = subprocess.run(
            [sys.executable, str(CONFIGURE)], text=True, capture_output=True,
            env=env, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        import tomllib as _tomllib
        text = config_path.read_text(encoding="utf-8")
        _tomllib.loads(text)
        self.assertEqual(text.count("a2a-hook.sh"), 4, "the managed blocks install amid hostile content")
        self.assertIn('command = "/home/agent/my-own-hook.sh"', text, "a hook that only mentions a2a-hook in a comment stays")
        self.assertIn("[thinking]", text, "an indented table after a comment carrying a delimiter survives")
        self.assertIn("enabled = true", text)
        self.assertIn("inside a literal string", text)
        self.assertIn("still the same string", text, "an escaped delimiter does not end a multiline string early")
        self.assertIn('run = ', text, "a closing quote run is not an opening")
        self.assertIn("a header-looking line inside a multiline string", text)
        result = subprocess.run(
            [sys.executable, str(CONFIGURE), "--remove"], text=True, capture_output=True,
            env=env, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        text = config_path.read_text(encoding="utf-8")
        _tomllib.loads(text)
        self.assertNotIn("a2a-hook.sh", text, "every managed block is gone")
        self.assertIn('command = "/home/agent/my-own-hook.sh"', text, "a hook that only mentions a2a-hook in a comment stays")
        self.assertIn("[thinking]", text, "an indented table after a comment carrying a delimiter survives")
        self.assertIn("enabled = true", text)
        self.assertIn("inside a literal string", text)
        self.assertIn("still the same string", text, "an escaped delimiter does not end a multiline string early")
        self.assertIn('run = ', text, "a closing quote run is not an opening")
        self.assertIn("a header-looking line inside a multiline string", text)

    def test_configure_installs_the_opencode_a2a_plugin(self):
        skills = self.root / "skills"
        for name in ("mempalace", "mempalace-recall", "mempalace-logstream", "a2a-chat"):
            target = skills / name
            target.mkdir(parents=True)
            (target / "SKILL.md").write_text(name, encoding="utf-8")
        env = dict(
            self.env,
            NOISEDECK_MEMPALACE_SKILLS_PATH=str(skills),
            NOISEDECK_MEMPALACE_PI_HOOK_PATH=str(HOOKS / "mempal-pi-hook.js"),
            NOISEDECK_A2A_PI_HOOK_PATH=str(HOOKS / "a2a-pi-hook.js"),
            NOISEDECK_A2A_OPENCODE_HOOK_PATH=str(HOOKS / "a2a-opencode-hook.js"),
        )
        plugin = self.home / ".config" / "opencode" / "plugins" / "a2a-opencode-hook.js"
        result = subprocess.run(
            [sys.executable, str(CONFIGURE)], text=True, capture_output=True,
            env=env, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(plugin.read_bytes(), (HOOKS / "a2a-opencode-hook.js").read_bytes())
        result = subprocess.run(
            [sys.executable, str(CONFIGURE), "--remove"], text=True, capture_output=True,
            env=env, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(plugin.exists(), "the plugin is removed with the rest of the surface")
        provider_plugin = self.home / ".config" / "opencode" / "plugins" / "noisedeck-provider-hook.js"
        provider_plugin.parent.mkdir(parents=True, exist_ok=True)
        provider_plugin.write_text("// provisioned identity plugin\n", encoding="utf-8")
        result = subprocess.run(
            [sys.executable, str(CONFIGURE), "--remove"], text=True, capture_output=True,
            env=env, check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(provider_plugin.exists(), "the provisioning-owned identity plugin is never touched")

    def test_logstream_skill_is_content_free_and_uses_git_for_patches(self):
        skill = (
            Path(__file__).parent / "skills" / "mempalace-logstream" / "SKILL.md"
        ).read_text(encoding="utf-8")
        self.assertIn("Omit `body`, `metadata`, and `artifact_ids` by default", skill)
        self.assertIn("Git remains the patch transport", skill)
        self.assertIn("never as authoritative proof", skill)
        self.assertIn("every 10 to 15 minutes", " ".join(skill.split()))
        self.assertIn("Do not create `.review`, `.reviews`", skill)
        self.assertNotIn("--body ", skill)
        self.assertNotIn("--metadata ", skill)

        dockerfile = (Path(__file__).parents[2] / "Dockerfile").read_text()
        self.assertIn(
            "COPY deploy/mempalace/skills/ /opt/mempalace/skills/", dockerfile
        )

        project_root = Path(__file__).parents[2]
        if not (project_root / "AGENTS.md").exists():
            self.skipTest("AGENTS.md is absent from the source export")
        agent_instructions = (project_root / "AGENTS.md").read_text()
        self.assertIn("`SDLC.md` in full", agent_instructions)
        project_instructions = agent_instructions + (project_root / "SDLC.md").read_text()
        # The review-lifecycle requirement moved from SDLC.md prose to the
        # dispatch-injected standing-focus file;
        # SDLC.md must name the file and the file must carry the text.
        self.assertIn(".noise/review-focus.md", project_instructions)
        review_focus = (project_root / ".noise" / "review-focus.md").read_text()
        self.assertIn("Use MemPalace Logstream for the review lifecycle", review_focus)
        self.assertNotIn("/workspace/deck/.review", project_instructions)
        self.assertNotIn("Review scratch files go", project_instructions)


if __name__ == "__main__":
    unittest.main()
