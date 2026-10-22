import assert from "node:assert/strict";
import test from "node:test";
import type { SessionSummary } from "../api/sessions.ts";
import { OutputActivityWindows } from "./output-activity.ts";

class FakeClock {
  next = 1;
  callbacks = new Map<number, () => void>();
  setTimeout(callback: () => void): number { const id = this.next++; this.callbacks.set(id, callback); return id; }
  clearTimeout(id: number): void { this.callbacks.delete(id); }
  fire(id: number): void { const callback = this.callbacks.get(id); this.callbacks.delete(id); callback?.(); }
}

const activityFacts = {
  recovery_capability: "none",
  recovery_state: "unavailable",
  presentation_state: "running",
} as const;

const sessions: SessionSummary[] = [
  { id: "one", project_id: "p", provider: "claude", display_name: "", status: "running", generation: 3, created_at: "t", updated_at: "t", ...activityFacts },
  { id: "two", project_id: "p", provider: "codex", display_name: "", status: "running", generation: 7, created_at: "t", updated_at: "t", ...activityFacts },
];

test("concurrent sessions own independent activity windows", () => {
  const clock = new FakeClock();
  const changes: string[] = [];
  const windows = new OutputActivityWindows(() => sessions, (id, active) => changes.push(`${id}:${active}`), clock);
  windows.ingest({ session_id: "one", generation: 3 });
  windows.ingest({ session_id: "two", generation: 7 });
  assert.deepEqual(changes, ["one:true", "two:true"]);
  clock.fire(1);
  assert.deepEqual(changes, ["one:true", "two:true", "one:false"]);
  windows.ingest({ session_id: "two", generation: 7 });
  assert.equal(clock.callbacks.has(2), false, "the second burst replaced only session two's timer");
  clock.fire(3);
  assert.equal(changes.at(-1), "two:false");
});

test("stale, exited, and unknown activity fails closed", () => {
  const clock = new FakeClock();
  const changes: string[] = [];
  const windows = new OutputActivityWindows(() => sessions, (id, active) => changes.push(`${id}:${active}`), clock);
  windows.ingest({ session_id: "one", generation: 2 });
  windows.ingest({ session_id: "missing", generation: 1 });
  sessions[0]!.status = "exited";
  windows.ingest({ session_id: "one", generation: 3 });
  assert.deepEqual(changes, []);
  sessions[0]!.status = "running";
});

test("dispose clears every active session without cross-session leakage", () => {
  const clock = new FakeClock();
  const changes: string[] = [];
  const windows = new OutputActivityWindows(() => sessions, (id, active) => changes.push(`${id}:${active}`), clock);
  windows.mark("one");
  windows.mark("two");
  windows.dispose();
  assert.deepEqual(changes.slice(-2).sort(), ["one:false", "two:false"]);
  assert.equal(clock.callbacks.size, 0);
});

test("reconcile clears activity when a session exits or disappears", () => {
  const clock = new FakeClock();
  const changes: string[] = [];
  const current = [...sessions];
  const windows = new OutputActivityWindows(() => current, (id, active) => changes.push(`${id}:${active}`), clock);
  windows.mark("one");
  windows.mark("two");
  current[0] = { ...current[0]!, status: "exited" };
  current.splice(1, 1);
  windows.reconcile();
  assert.deepEqual(changes.slice(-2).sort(), ["one:false", "two:false"]);
  assert.equal(clock.callbacks.size, 0);
});
