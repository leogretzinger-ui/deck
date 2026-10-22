import assert from "node:assert/strict";
import test from "node:test";
import { CompletionReads } from "./completion-reads.ts";
import type { SessionSummary } from "../api/sessions.ts";

const session = (): SessionSummary => ({ id: "s1", generation: 1, attention_state: "approval", completion_revision: "a".repeat(32), completion_unread: true } as SessionSummary);

test("hidden or undisplayed selection keeps completion unread; pending approval survives viewing", async () => {
  const sent: unknown[] = [];
  const reads = new CompletionReads(async (...args) => { sent.push(args); });
  const row = session();
  await reads.observe(row, false, () => {});
  assert.equal(sent.length, 0);
  assert.equal(row.completion_unread, true);
  await reads.observe(row, true, () => {});
  assert.deepEqual(sent, [["s1", 1, "a".repeat(32)]]);
  assert.equal(row.completion_unread, false);
  assert.equal(row.attention_state, "approval");
  const stalePoll = session();
  await reads.observe(stalePoll, true, () => {});
  assert.equal(stalePoll.completion_unread, false);
  assert.equal(sent.length, 1);
});
test("failed acknowledgments retry and a new revision racing the reply stays unread", async () => {
  let finish: (() => void) | undefined;
  let calls = 0;
  const reads = new CompletionReads(() => { calls++; return calls === 1 ? Promise.reject(new Error("offline")) : new Promise(resolve => { finish = resolve; }); });
  const row = session();
  await reads.observe(row, true, () => {});
  assert.equal(row.completion_unread, true);
  const request = reads.observe(row, true, () => {});
  await reads.observe(row, true, () => {});
  assert.equal(calls, 2, "deduplicates in-flight polls");
  row.completion_revision = "b".repeat(32);
  finish!();
  await request;
  assert.equal(row.completion_unread, true);
});
test("absent state and disposal cause no acknowledgment or late redraw", async () => {
  let finish: (() => void) | undefined;
  const reads = new CompletionReads(() => new Promise(resolve => { finish = resolve; }));
  let renders = 0;
  await reads.observe(undefined, true, () => { renders++; });
  const row = session();
  const request = reads.observe(row, true, () => { renders++; });
  reads.dispose(); finish!(); await request;
  assert.equal(renders, 0);
  assert.equal(row.completion_unread, true);
});
