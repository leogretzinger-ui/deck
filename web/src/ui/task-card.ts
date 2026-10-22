import type { TmTask } from "../api/tasks.ts";
import { escapeHtml } from "../lib/escape.ts";
import {
  priorityDotClass,
  priorityLabel,
  taskID,
  taskStatusFacts,
} from "../lib/task-facts.ts";

export const TASK_REF_ATTR = "data-task-ref";

// escapedText has already passed through the markdown renderer's untrusted-text boundary.
export function taskRefLinkHtml(escapedText: string, id: string): string {
  return `<span ${TASK_REF_ATTR}="${escapeHtml(id)}" tabindex="0"
    aria-label="Task ${escapeHtml(id)}"
    class="cursor-help rounded-mark bg-surface-muted px-1 font-medium text-text-primary underline decoration-dotted underline-offset-2">${escapedText}</span>`;
}

// interactive=false renders resolvable dependencies as inert text rather
// than tabbable references: the card is a <button>, so a nested focusable
// element would both be invalid markup and add a keyboard stop. The detail
// dialog keeps the interactive form.
export function taskDependenciesHtml(
  dependencies: TmTask["dependencies"],
  resolvable: ReadonlySet<string>,
  interactive = true,
): string {
  if (!dependencies || dependencies.length === 0) return "";
  return dependencies.map((raw) => {
    const id = taskID(raw);
    return interactive && resolvable.has(id)
      ? taskRefLinkHtml(escapeHtml(id), id)
      : `<span data-task-dependency-inert>${escapeHtml(id)}</span>`;
  }).join(", ");
}

export function taskCardPopoverHtml(t: TmTask, resolvable: ReadonlySet<string> = new Set()): string {
  const id = escapeHtml(taskID(t.id));
  const { label, dot } = taskStatusFacts(t);
  const deps = taskDependenciesHtml(t.dependencies, resolvable);
  return `
    <div class="flex items-center gap-2">
      <span class="font-mono text-caption text-text-tertiary">${id}</span>
      ${t.priority
        ? `<span class="flex flex-none items-center gap-[5px]"><span aria-hidden="true" class="h-1.5 w-1.5 flex-none rounded-full ${priorityDotClass(t.priority)}"></span><span class="priority-label text-text-tertiary">${escapeHtml(priorityLabel(t.priority))}</span></span>`
        : ""}
      <span class="ml-auto flex items-center gap-1.5">
        <span aria-hidden="true" class="h-1.5 w-1.5 flex-none rounded-full ${dot}"></span>
        <span class="text-micro text-text-tertiary">${escapeHtml(label)}</span>
      </span>
    </div>
    <span class="block w-full text-body font-medium leading-[1.4] text-text-primary [text-wrap:pretty]">${escapeHtml(t.title)}</span>
    ${deps ? `<span data-task-dependencies class="block truncate text-micro leading-[1.4] text-text-tertiary">depends on <span class="font-mono">${deps}</span></span>` : ""}
  `;
}

export const TASK_POPOVER_CLASS =
  "absolute z-30 flex w-[260px] max-w-full flex-col gap-2 rounded-card border border-border-strong bg-surface-card px-[14px] py-3 text-left shadow-lg";
