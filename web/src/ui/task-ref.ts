import type { TmTask } from "../api/tasks.ts";
import { taskID } from "../lib/task-facts.ts";

export interface TaskRefMatch {
  id: string;
  start: number;
  end: number;
}

const TASK_REF_RE = /task(?:[ \t]+|-)(\d+)/gi;
const WORD_CHAR_RE = /[\p{L}\p{N}_]/u;

export function findTaskRefs(text: string): TaskRefMatch[] {
  const out: TaskRefMatch[] = [];
  TASK_REF_RE.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = TASK_REF_RE.exec(text)) !== null) {
    const start = m.index;
    const end = TASK_REF_RE.lastIndex;
    if (WORD_CHAR_RE.test(codePointBefore(text, start))) continue;
    if (WORD_CHAR_RE.test(codePointAt(text, end))) continue;
    if (text[end] === "." && /\d/.test(text[end + 1] ?? "")) continue;
    out.push({ id: m[1]!, start, end });
  }
  return out;
}

function codePointBefore(text: string, index: number): string {
  if (index === 0) return "";
  const low = text.charCodeAt(index - 1);
  if (low >= 0xDC00 && low <= 0xDFFF && index >= 2) {
    const high = text.charCodeAt(index - 2);
    if (high >= 0xD800 && high <= 0xDBFF) return text.slice(index - 2, index);
  }
  return text[index - 1] ?? "";
}

function codePointAt(text: string, index: number): string {
  const point = text.codePointAt(index);
  return point === undefined ? "" : String.fromCodePoint(point);
}

export function collectTaskIDs(tags: [string, TmTask[]][]): Set<string> {
  const ids = new Set<string>();
  const walk = (tasks: TmTask[]): void => {
    for (const t of tasks) {
      ids.add(taskID(t.id));
      if (t.subtasks) walk(t.subtasks);
    }
  };
  for (const [, tasks] of tags) walk(tasks);
  return ids;
}

export function resolveTask(id: string, tags: [string, TmTask[]][]): TmTask | null {
  const walk = (tasks: TmTask[]): TmTask | null => {
    for (const t of tasks) {
      if (taskID(t.id) === id) return t;
      const hit = t.subtasks ? walk(t.subtasks) : null;
      if (hit !== null) return hit;
    }
    return null;
  };
  for (const [, tasks] of tags) {
    const hit = walk(tasks);
    if (hit !== null) return hit;
  }
  return null;
}
