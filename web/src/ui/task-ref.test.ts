import assert from "node:assert/strict";
import test from "node:test";

import { collectTaskIDs, findTaskRefs, resolveTask } from "./task-ref.ts";
import type { TmTask } from "../api/tasks.ts";

function refs(text: string): { id: string; text: string }[] {
  return findTaskRefs(text).map((r) => ({ id: r.id, text: text.slice(r.start, r.end) }));
}

test("recognizes 'Task 126' and 'task-126', case-insensitively", () => {
  assert.deepEqual(refs("see Task 126 for details"), [{ id: "126", text: "Task 126" }]);
  assert.deepEqual(refs("see task-126 for details"), [{ id: "126", text: "task-126" }]);
  assert.deepEqual(refs("TASK 9"), [{ id: "9", text: "TASK 9" }]);
  assert.deepEqual(refs("Task-9"), [{ id: "9", text: "Task-9" }]);
});

test("captures the full digit run as the id", () => {
  assert.deepEqual(refs("Task 1260"), [{ id: "1260", text: "Task 1260" }]);
  assert.deepEqual(refs("task-1260"), [{ id: "1260", text: "task-1260" }]);
});

test("finds multiple references on one line", () => {
  assert.deepEqual(
    refs("Task 1 blocks task-2 and Task 3"),
    [
      { id: "1", text: "Task 1" },
      { id: "2", text: "task-2" },
      { id: "3", text: "Task 3" },
    ],
  );
});

test("a bare '#126' is not a task reference", () => {
  assert.deepEqual(refs("fixed in #126"), []);
  assert.deepEqual(refs("see #126 and #1"), []);
});

test("a partial-word 'task' does not match", () => {
  assert.deepEqual(refs("multitask 126"), [], "multitask is not task");
  assert.deepEqual(refs("2task-126"), []);
  assert.deepEqual(refs("tasks 126"), [], "the plural is its own word");
  assert.deepEqual(refs("tasking 126"), []);
  assert.deepEqual(refs("mytask-126"), []);
  assert.deepEqual(refs("étask-126"), []);
  assert.deepEqual(refs("𐐀task-126"), []);
  assert.deepEqual(refs("Task 126abc"), []);
  assert.deepEqual(refs("Task 126_foo"), []);
  assert.deepEqual(refs("Task 126𐐀"), []);
});

test("a reference needs the separator, not just 'task' next to digits", () => {
  assert.deepEqual(refs("task126"), [], "no separator");
  assert.deepEqual(refs("task_126"), []);
  assert.deepEqual(refs("task.126"), []);
});

test("preceding punctuation does not block a real reference", () => {
  assert.deepEqual(refs("(Task 126)"), [{ id: "126", text: "Task 126" }]);
  assert.deepEqual(refs('"task-126"'), [{ id: "126", text: "task-126" }]);
  assert.deepEqual(refs("Task 126:"), [{ id: "126", text: "Task 126" }]);
  assert.deepEqual(refs("- Task 126"), [{ id: "126", text: "Task 126" }]);
});

test("match offsets are exact for slicing", () => {
  const text = "lead-in Task 126 trailing";
  for (const r of findTaskRefs(text)) {
    assert.equal(text.slice(r.start, r.end), "Task 126");
  }
});

test("a dotted subtask id is not partially linkified as its parent", () => {
  assert.deepEqual(refs("Task 2.1"), []);
});

const sampleTags: [string, TmTask[]][] = [
  ["master", [
    { id: "1", title: "first", status: "done" },
    { id: "126", title: "the one", status: "in-progress", priority: "high" },
    {
      id: "2",
      title: "second",
      status: "pending",
      subtasks: [{ id: "2.1", title: "sub", status: "pending" }],
    },
  ]],
];

test("collectTaskIDs includes top-level and qualified subtask ids", () => {
  const ids = collectTaskIDs(sampleTags);
  assert.ok(ids.has("1"));
  assert.ok(ids.has("126"));
  assert.ok(ids.has("2.1"));
});

test("resolveTask returns the task for a known id", () => {
  const t = resolveTask("126", sampleTags);
  assert.ok(t !== null);
  assert.equal(t!.title, "the one");
});

test("resolveTask returns null for an unknown id (fail closed)", () => {
  assert.equal(resolveTask("9999", sampleTags), null);
  assert.equal(resolveTask("", sampleTags), null);
});

test("resolveTask does not match a dotted subtask from a bare number", () => {
  assert.equal(resolveTask("2", sampleTags)?.title, "second");
});
