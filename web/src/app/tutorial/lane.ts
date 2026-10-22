import { ApiError } from "../../api/http.ts";
import type { TmTask } from "../../api/tasks.ts";
import { taskID } from "../../lib/task-facts.ts";

// Pure choices the tutorial makes about lanes: which task a lane is for, what
// a reviewer is asked, and how a refusal is told. No DOM, no terminal.

export interface LaneTask {
  tag: string;
  // Lanes work on top-level tasks, which have a numeric id.
  id: number;
  title: string;
  description: string;
  revision: number | undefined;
}

type Tags = readonly (readonly [string, readonly TmTask[]])[];

function numericID(task: TmTask): number | null {
  if (task.parentId !== undefined && task.parentId !== null) return null;
  const id = taskID(task.id);
  return /^[1-9]\d*$/.test(id) ? Number(id) : null;
}

function laneTask(tag: string, task: TmTask): LaneTask | null {
  const id = numericID(task);
  return id === null ? null : { tag, id, title: task.title, description: task.description ?? "", revision: task.revision };
}

// The next task a lane can start on: pending, unclaimed, and with every
// dependency done, in the engine's own order.
export function pickLaneTask(tags: Tags): LaneTask | null {
  for (const [tag, tasks] of tags) {
    const done = new Set(tasks.filter((task) => task.status === "done").map((task) => taskID(task.id)));
    for (const task of tasks) {
      if (task.status !== "pending" || task.claim !== undefined) continue;
      if ((task.dependencies ?? []).some((dependency) => !done.has(String(dependency)))) continue;
      const picked = laneTask(tag, task);
      if (picked !== null) return picked;
    }
  }
  return null;
}

// The task to close when no lane names one: the only task in progress, else
// the only task that is not done. Anything less certain is no answer.
export function pickOpenTask(tags: Tags): LaneTask | null {
  const open: LaneTask[] = [];
  const working: LaneTask[] = [];
  for (const [tag, tasks] of tags) {
    for (const task of tasks) {
      if (task.status === "done" || task.status === "cancelled") continue;
      const picked = laneTask(tag, task);
      if (picked === null) continue;
      open.push(picked);
      if (task.status === "in-progress") working.push(picked);
    }
  }
  if (working.length === 1) return working[0]!;
  return working.length === 0 && open.length === 1 ? open[0]! : null;
}

export function findLaneTask(tags: Tags, tag: string, id: number): TmTask | null {
  const tasks = tags.find(([name]) => name === tag)?.[1] ?? [];
  return tasks.find((task) => numericID(task) === id) ?? null;
}

// The agents a review can come from: everything this image can launch except
// a plain shell and the agent that wrote the change.
export function reviewCandidates(providers: readonly string[], author: string): string[] {
  return providers.filter((name) => name !== "shell" && name !== author);
}

// Typed into the reviewer's terminal without Return. It is one line because
// typed input carries no newline.
export function reviewPrompt(taskTitle: string | null): string {
  const about = taskTitle === null || taskTitle === "" ? "" : ` The change is for this task: "${taskTitle}".`;
  return "Another agent wrote the change on this branch and opened a pull request for it. Read the pull request with gh pr view and gh pr diff, "
    + `review it for correctness, and post what you find as a pull request comment with gh pr comment. Do not change any files.${about}`;
}

// A refusal as one sentence the panel can show.
export function laneProblem(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return err instanceof Error ? err.message : String(err);
}
