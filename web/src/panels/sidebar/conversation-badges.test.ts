import assert from "node:assert/strict";
import test from "node:test";
import { sessionMetaHtml } from "./sidebar.ts";
import type { SessionSummary } from "../../api/sessions.ts";

const row = { id: "s1", presentation_state: "running", last_output_at: "2026-10-22T10:00:00Z" } as SessionSummary;
test("native attention and completion badges are distinct, accessible, and independent of output/chat", () => {
  for (const [state, label] of [["input", "Agent requests input"], ["approval", "Agent requests approval"]] as const) {
    const html = sessionMetaHtml({ ...row, attention_state: state, completion_unread: true, completion_revision: "a" }, true, "unseen");
    assert.match(html, new RegExp(`data-conversation-badge="${state}" role="img" aria-label="${label}" title="${label}"`));
    assert.match(html, /data-meta-chat="unseen"/);
    assert.match(html, /output streaming/);
    assert.doesNotMatch(html, /data-conversation-badge="completed"/);
  }
  const completed = sessionMetaHtml({ ...row, completion_unread: true, completion_revision: "a" }, false);
  assert.match(completed, /data-conversation-badge="completed"/);
  assert.match(completed, /not task success/);
});
test("unknown, unsupported, absent, or seen completion never invents a badge", () => {
  for (const data of [{}, { attention_state: "unknown" }, { attention_state: "working" }, { completion_unread: false, completion_revision: "a" }, { completion_unread: true }]) {
    assert.doesNotMatch(sessionMetaHtml({ ...row, ...data } as SessionSummary, false), /data-conversation-badge/);
  }
  const unavailable = sessionMetaHtml({ ...row, presentation_state: "attachment_unavailable" }, false);
  assert.match(unavailable, /data-meta-attention/);
  assert.doesNotMatch(unavailable, /data-conversation-badge/);
});
