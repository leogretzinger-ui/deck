#!/usr/bin/env python3
"""Drive the real a2a OpenCode plugin with a stub session-server client.

The serve-mode integration probe (probe_a2a_receive.py --harness opencode)
exercises the whole harness; this one isolates the plugin's own delivery
logic — adoption, the idle gate, the inbox poll, the prompt submission, the
receipt and the acknowledgement — deterministically, with no opencode
bootstrap in the way. It fails if any step of the receive contract regresses.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from probe_a2a_receive import HOOKS, INVITE_BODY, MAILBOX  # noqa: E402

DRIVE = """
import { A2AChat } from '__PLUGIN__';
const root = '__ROOT__';
const BODY = __BODY__;
const calls = [];
const state = { admitted: [] };
const client = {
  session: {
    get: async ({ path }) => ({ data: { id: path.id, parentID: undefined } }),
    message: async ({ path }) => {
      const found = state.admitted.filter((m) => m.id === path.messageID);
      return found.length ? { data: found[0] } : { error: 'not found' };
    },
    promptAsync: async ({ path, body }) => {
      const parts = process.env.PROBE_STRIP_TEXT === '1'
        ? body.parts.map((p) => ({ ...p, text: '' }))
        : body.parts;
      calls.push(['prompt', path.id, body.messageID]);
      state.admitted.push({ id: body.messageID, parts });
      return { data: {} };
    },
  },
};
const append = (line) => process.stdout.write(line + '\\n');
const plugin = await A2AChat({ client, directory: root + '/cwd' });
const events = [
  { type: 'session.created', properties: { info: { id: 'ses_probe1' } } },
  { type: 'session.idle', properties: { sessionID: 'ses_probe1' } },
];
for (const e of events) {
  await plugin.event({ event: e });
}
// First the wake path — the follower hands the plugin events directly:
// this is exactly what one stdout line of `a2a inbox --follow --json`
// delivers, with no peek in between.
const wake = [
  { id: 'w1', type: 'chat.invite', from_agent: 'fixture', correlation_id: 'chat-fixture',
    scope: 'chat-fixture|invite', body: BODY },
];
await plugin.deliverEvents(wake);
append('wake-submitted=' + JSON.stringify(calls));
for (let i = 0; i < 100 && !state.admitted.length; i++) {
  await new Promise((resolve) => setTimeout(resolve, 100));
}
append('prompts=' + JSON.stringify(calls));
append('parts=' + JSON.stringify(state.admitted.flatMap((m) => m.parts.map((p) => p.text))));
append('strip=' + (process.env.PROBE_STRIP_TEXT === '1'));
"""


def drive_once(root: Path, strip: bool = False) -> str:
    drive = (DRIVE.replace("__PLUGIN__", str(HOOKS / "a2a-opencode-hook.js"))
             .replace("__ROOT__", str(root)).replace("__BODY__", json.dumps(INVITE_BODY)))
    env = {key: value for key, value in os.environ.items() if key in ("PATH",)}
    env.update(
        HOME=str(root),
        A2A_BIN=str(root / "mail.py"),
        A2A_STATE_DIR=str(root / "state"),
        NOISEDECK_MEMPALACE_PYTHON="/usr/bin/python3",
        A2A_OPENCODE_POLL_MS="100",
        # The drive exercises the wake and peek paths directly; a real
        # follower child would outlive the scripted drive.
        A2A_OPENCODE_FOLLOW="0",
    )
    if strip:
        env["PROBE_STRIP_TEXT"] = "1"
    done = subprocess.run(
        ["node", "--input-type=module", "-e", drive],
        env=env, cwd=str(root), capture_output=True, text=True, timeout=90,
    )
    if done.returncode != 0:
        raise SystemExit(f"FAIL [opencode-plugin]: the plugin drive crashed\n{done.stderr[-2000:]}")
    return done.stdout


def delivered_text(stdout: str) -> str:
    """The text parts the session was prompted with, joined."""
    for line in stdout.splitlines():
        if line.startswith("parts="):
            try:
                return "\n".join(json.loads(line[len("parts="):]))
            except json.JSONDecodeError:
                return ""
    return ""


def check(root: Path, stdout: str) -> None:
    if "ses_probe1" not in stdout:
        raise SystemExit(
            "FAIL [opencode-plugin]: the plugin never prompted the session\n"
            f"stdout: {stdout[-1000:]}"
        )
    text = delivered_text(stdout)
    if "[a2a" not in text or INVITE_BODY not in text:
        raise SystemExit(
            "FAIL [opencode-plugin]: the prompt carried no wake text\n"
            f"delivered parts: {text[-500:]!r}"
        )
    if not (root / "acked").exists():
        raise SystemExit(
            "FAIL [opencode-plugin]: the invitation was never acknowledged (mark-read missing)\n"
            f"calls: {(root / 'calls').read_text() if (root / 'calls').exists() else '(none)'}"
        )


def main() -> None:
    root = Path(tempfile.mkdtemp(prefix="a2a-plugin-opencode-"))
    for name in ("cwd", "state"):
        (root / name).mkdir()
    (root / "mail.py").write_text(MAILBOX.replace("__BODY__", json.dumps(INVITE_BODY)).replace("__DELAY__", "0"))
    try:
        check(root, drive_once(root))
        # Negative control: a prompt whose text is emptied must fail the same
        # assertion, proving the check reads the delivered text, not the ids.
        negative = Path(tempfile.mkdtemp(prefix="a2a-plugin-opencode-neg-", dir=root.parent))
        for name in ("cwd", "state"):
            (negative / name).mkdir()
        (negative / "mail.py").write_text(MAILBOX.replace("__BODY__", json.dumps(INVITE_BODY)).replace("__DELAY__", "0"))
        try:
            check(negative, drive_once(negative, strip=True))
        except SystemExit as exc:
            if "carried no wake text" not in str(exc):
                raise
        else:
            raise SystemExit(
                "FAIL [opencode-plugin]: an emptied prompt passed the text assertion"
            )
        print("PASS [opencode-plugin]: adoption, idle poll, session prompt with wake text, "
              "receipt and acknowledgement all ran against the real plugin module")
    finally:
        subprocess.run(["rm", "-rf", str(root)], check=False)


if __name__ == "__main__":
    main()
