import { test } from "node:test";
import assert from "node:assert/strict";
import { runtimeHtml, type RuntimeView } from "./runtime.ts";
const base: RuntimeView = { name: "workspace", address: "workspace.example", methods: ["passkey", "password", "recovery"], screen: { kind: "locked" } };

test("runtime metadata is omitted until an authoritative observation supplies it", () => {
  for (const screen of [{ kind: "locked", confirmed: true }, { kind: "unavailable" }, { kind: "lock-confirm" }, { kind: "lock-failed" }, { kind: "progress", operation: "lock", storage: "open", runtime: "stopping" }] as const) {
    const html = runtimeHtml({ ...base, screen });
    assert.doesNotMatch(html, /owner@|op-4412|rewrite-scheduler|flaky-test-hunt|Last reached|Storage closed at|Two sessions/);
  }
  assert.match(runtimeHtml({ ...base, screen: { kind: "locked", confirmed: true } }), /Storage is closed and the workspace has stopped/);
});
test("new browser authorization exposes no session metadata or start claim", () => {
  const html = runtimeHtml({ ...base, screen: { kind: "authorize-browser" }, sessions: [{ name: "private-name", command: "private-command", age: "1m", state: "running" }] });
  assert.doesNotMatch(html, /private-name|private-command/);
  assert.match(html, /Verify with passkey/);
  assert.match(html, /nothing stops and nothing restarts/);
});
test("uncertain lock can only observe and help does not invent provisioning", () => {
  const html = runtimeHtml({ ...base, screen: { kind: "unknown", operation: "lock" } });
  assert.match(html, /Reconnect and check/);
  assert.doesNotMatch(html, /data-security-action="confirm-lock"|land back in the workspace/);
  assert.match(runtimeHtml({ ...base, screen: { kind: "lost-methods" } }), /If every unlock method/);
  assert.doesNotMatch(runtimeHtml({ ...base, screen: { kind: "lost-methods" } }), /Start a new workspace|Ask whoever runs the machine to delete/);
});
