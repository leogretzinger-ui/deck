import assert from "node:assert/strict";
import test from "node:test";

import {
  TASK_POPOVER_CLASS,
  taskCardPopoverHtml,
  taskDependenciesHtml,
} from "./task-card.ts";

test("task previews use shared semantic theme tokens", () => {
  for (const token of [
    "border-border-strong",
    "bg-surface-card",
    "text-left",
  ]) {
    assert.ok(TASK_POPOVER_CLASS.includes(token), `${token} missing`);
  }
  const html = taskCardPopoverHtml({
    id: "203",
    title: "Theme-correct preview",
    status: "in-progress",
    priority: "high",
    dependencies: ["200"],
  }, new Set(["200"]));
  assert.match(html, /text-text-primary/);
  assert.match(html, /text-text-tertiary/);
  assert.match(html, /bg-error/);
  assert.match(html, /bg-accent/);
  assert.match(html, /class="[^"]*priority-label[^"]*">high</, "the priority label is uppercased by CSS, stays lowercase in the markup, and uses the normal line box");
});

test("each resolvable dependency is an independent task reference", () => {
  const html = taskDependenciesHtml(["1", "2", "404"], new Set(["1", "2"]));
  assert.match(html, /data-task-ref="1"[^>]*tabindex="0"[^>]*aria-label="Task 1"/);
  assert.match(html, /data-task-ref="2"[^>]*tabindex="0"[^>]*aria-label="Task 2"/);
  assert.match(html, /data-task-dependency-inert>404<\/span>/);
  assert.equal((html.match(/data-task-ref=/g) ?? []).length, 2);
});

test("a card's resolvable dependencies are inert text, never tabbable", () => {
  const html = taskDependenciesHtml(["1", "2", "404"], new Set(["1", "2"]), false);
  assert.equal((html.match(/data-task-ref=/g) ?? []).length, 0, "no reference markup");
  assert.equal(html.includes("tabindex"), false, "nothing is focusable");
  assert.match(html, /data-task-dependency-inert>1<\/span>/);
  assert.match(html, /data-task-dependency-inert>2<\/span>/);
  assert.match(html, /data-task-dependency-inert>404<\/span>/);
});
