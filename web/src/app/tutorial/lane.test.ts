import assert from "node:assert/strict";
import test from "node:test";
import type { TmTask } from "../../api/tasks.ts";
import { pickLaneTask, pickOpenTask, reviewCandidates, reviewPrompt } from "./lane.ts";

const task = (id: unknown, status: string, extra: Partial<TmTask> = {}): TmTask => ({ id, title: `Task ${String(id)}`, status, ...extra });

test("a lane starts on the first pending, unclaimed task whose dependencies are done", () => {
  const tags = [["main", [
    task("1", "done"),
    task("2", "pending", { claim: { owner: "x", acquiredAt: "", expiresAt: "" } }),
    task("3", "pending", { dependencies: ["9"] }),
    task("4", "pending", { dependencies: ["1"], description: "Do it", revision: 3 }),
    task("5", "pending"),
  ]]] as const;
  assert.deepEqual(pickLaneTask(tags), { tag: "main", id: 4, title: "Task 4", description: "Do it", revision: 3 });
  assert.equal(pickLaneTask([["main", [task("1", "done"), task("2", "in-progress")]]]), null);
});

test("a subtask or a non-numeric id never gets a lane", () => {
  assert.equal(pickLaneTask([["main", [task("1.2", "pending"), task("a", "pending"), task(7, "pending", { parentId: 1 })]]]), null);
  assert.equal(pickLaneTask([["main", [task(7, "pending")]]])?.id, 7);
});

test("the task to close without a lane is the only one in progress, else the only one open", () => {
  assert.equal(pickOpenTask([["main", [task("1", "in-progress"), task("2", "pending")]]])?.id, 1);
  assert.equal(pickOpenTask([["main", [task("1", "done"), task("2", "pending")]]])?.id, 2);
  assert.equal(pickOpenTask([["main", [task("1", "pending"), task("2", "pending")]]]), null);
  assert.equal(pickOpenTask([["main", [task("1", "in-progress"), task("2", "in-progress")]]]), null);
  assert.equal(pickOpenTask([["main", [task("1", "done")]]]), null);
});

test("a reviewer is any launchable agent except a shell and the author", () => {
  assert.deepEqual(reviewCandidates(["claude", "codex", "shell", "kimi"], "claude"), ["codex", "kimi"]);
  assert.deepEqual(reviewCandidates(["claude", "shell"], "claude"), []);
});

test("the review prompt is one line, names the pull request commands, and asks for a comment", () => {
  const text = reviewPrompt("Print the Breaking changes section");
  assert.ok(!/[\u0000-\u001f\u007f-\u009f]/u.test(text), "typed input carries no control characters");
  for (const part of ["gh pr view", "gh pr diff", "gh pr comment", "correctness", "Print the Breaking changes section"]) assert.ok(text.includes(part), part);
  assert.ok(!reviewPrompt(null).includes("task:"));
});
