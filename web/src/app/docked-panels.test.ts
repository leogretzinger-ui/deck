// The docked-panel coordinator. There is no jsdom here, so the hosts are
// stubs that record the one attribute the coordinator writes — which is
// all the CSS reads to decide whether a panel is laid out.
import assert from "node:assert/strict";
import test from "node:test";

import { DockedPanels, type DockedPanelName, type OpenPanel } from "./docked-panels.ts";

interface Host {
  open: string | null;
  hidden: string | null;
  inert: boolean;
  setAttribute(name: string, value: string): void;
}

function hosts(): {
  panels: DockedPanels;
  files: Host;
  tasks: Host;
  chats: Host;
  assets: Host;
  events: string[];
  changes: OpenPanel[];
  missing(): void;
} {
  const make = (): Host => ({
    open: null,
    hidden: null,
    inert: false,
    setAttribute(name: string, value: string) {
      if (name === "data-open") this.open = value;
      if (name === "aria-hidden") this.hidden = value;
    },
  });
  const files = make();
  const tasks = make();
  const chats = make();
  const assets = make();
  let present = true;
  const events: string[] = [];
  const changes: OpenPanel[] = [];
  const panels = new DockedPanels(
    (name) => {
      if (!present) return null;
      return ({ files, tasks, chats, assets }[name]) as unknown as HTMLElement;
    },
    {
      onOpen: (name) => events.push(`open:${name}`),
      onClose: (name) => events.push(`close:${name}`),
      onChange: (open) => changes.push(open),
    },
  );
  return { panels, files, tasks, chats, assets, events, changes, missing: () => { present = false; } };
}

test("chats is a third sibling in the same row: opening it closes the other two", () => {
  const { panels, files, tasks, chats } = hosts();
  panels.set("tasks");
  panels.set("chats");
  assert.deepEqual([files.open, tasks.open, chats.open], ["false", "false", "true"]);
  assert.deepEqual([files.inert, tasks.inert, chats.inert], [true, true, false]);
  panels.toggle("chats");
  assert.equal(panels.open, null);
  assert.equal(chats.open, "false");
});

test("assets is a fourth sibling in the same row: opening it closes the other three", () => {
  const { panels, files, tasks, chats, assets } = hosts();
  panels.set("chats");
  panels.set("assets");
  assert.deepEqual([files.open, tasks.open, chats.open, assets.open], ["false", "false", "false", "true"]);
  assert.deepEqual([files.inert, tasks.inert, chats.inert, assets.inert], [true, true, true, false]);
  panels.toggle("assets");
  assert.equal(panels.open, null);
  assert.equal(assets.open, "false");
});

test("exactly one panel is open, in both directions", () => {
  const { panels, files, tasks } = hosts();
  for (const [first, second] of [["files", "tasks"], ["tasks", "files"]] as const) {
    panels.set(first);
    assert.equal(panels.open, first);
    assert.equal(panels.isOpen(second), false);
    panels.set(second);
    assert.equal(panels.open, second);
    assert.equal(panels.isOpen(first), false);
  }
  assert.deepEqual([files.open, tasks.open], ["true", "false"], "the last set won alone");
  panels.close();
  assert.equal(panels.open, null);
  assert.equal(files.open, "false");
  assert.equal(tasks.open, "false");
});

test("the attribute is written for BOTH panels on every change", () => {
  // A coordinator that only touched the panel it was asked about would
  // leave the other one laid out: two panels in one row.
  const { panels, files, tasks } = hosts();
  panels.set("files");
  assert.deepEqual([files.open, tasks.open], ["true", "false"]);
  assert.deepEqual([files.hidden, tasks.hidden], ["false", "true"]);
  assert.deepEqual([files.inert, tasks.inert], [false, true]);
  panels.set("tasks");
  assert.deepEqual([files.open, tasks.open], ["false", "true"]);
});

test("toggle closes the panel that is already open", () => {
  const { panels } = hosts();
  panels.toggle("tasks");
  assert.equal(panels.open, "tasks");
  panels.toggle("tasks");
  assert.equal(panels.open, null);
  panels.toggle("tasks");
  panels.toggle("files");
  assert.equal(panels.open, "files", "toggling the other panel switches rather than closes");
});

test("open and close fire once per real change; change fires every time", () => {
  const { panels, events, changes } = hosts();
  panels.set("files");
  panels.set("files");
  panels.set("tasks");
  panels.close();
  assert.deepEqual(events, ["open:files", "close:files", "open:tasks", "close:tasks"]);
  assert.deepEqual(changes, ["files", "files", "tasks", null]);
});

// Reopening a panel must re-read canonical state, so a repeated open IS a
// transition — but reapply(), which exists to push state onto a freshly
// mounted region, must not be one.
test("reapply repaints without firing a transition", () => {
  const { panels, tasks, events } = hosts();
  panels.set("tasks");
  tasks.open = null;
  panels.reapply();
  assert.equal(tasks.open, "true");
  assert.deepEqual(events, ["open:tasks"]);
});

// After auth teardown the root no longer carries workspace regions, so
// closing has to be a no-op rather than a throw.
test("a missing region is a no-op, and the state still tracks", () => {
  const { panels, missing } = hosts();
  panels.set("files");
  missing();
  assert.doesNotThrow(() => panels.close());
  assert.equal(panels.open, null);
});

test("the coordinator never reports a panel name it was not given", () => {
  const { panels } = hosts();
  const names: DockedPanelName[] = ["files", "tasks"];
  panels.set("files");
  assert.deepEqual(names.filter((n) => panels.isOpen(n)), ["files"]);
});
