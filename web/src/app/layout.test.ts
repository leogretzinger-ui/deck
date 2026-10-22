// The workspace frame's markup contract: the regions the shell mounts
// into, and the docked geometry both panels declare. The resolved pixels
// are the browser probes' job (tools/files-panel-probe.mjs,
// tools/tasks-panel-probe.mjs); these are the declarations that produce
// them, pinned so a class rewrite cannot silently move a panel back on
// top of the terminal.
import assert from "node:assert/strict";
import test from "node:test";

import { workspaceLayout } from "./layout.ts";

// The regions the shell mounts components into must exist in the markup.
test("the layout renders every region the shell mounts into", () => {
  const html = workspaceLayout();
  for (const region of [
    "terminal-card",
    "terminal",
    "terminal-soft-keys",
    "tasks",
    "sidebar",
    "content",
    "files",
  ]) {
    assert.ok(html.includes(`data-region="${region}"`), `layout is missing ${region}`);
  }
});

function aside(html: string, region: string): string {
  const match = new RegExp(`<aside data-region="${region}"[^>]*>`).exec(html);
  assert.ok(match !== null, `no ${region} aside in ${html}`);
  return match[0];
}

// Both panels are DOCKED splits, so what neither may do is take the
// terminal's space entirely or cover it.
test("both docked panels start closed, inert, and dock beside the terminal", () => {
  const html = workspaceLayout();
  for (const region of ["files", "tasks"] as const) {
    const el = aside(html, region);
    assert.ok(el.includes('data-open="false"'), `${region} starts closed`);
    assert.ok(el.includes('aria-hidden="true"'), `${region} starts outside the accessibility tree`);
    assert.match(el, /\sinert(?:\s|>)/, `${region} starts unfocusable`);
    assert.ok(el.includes("flex-none"), `${region} owns docked layout width`);
    assert.ok(el.includes("border-l"), `${region} draws the seam against the terminal`);
    assert.ok(el.includes("h-full"), `${region} fills the content height`);
    assert.equal(/fixed|absolute|inset-0/.test(el), false, `${region} is docked, not an overlay`);
  }
  // The only scrim in the frame belongs to the mobile sidebar; neither
  // docked panel is modal.
  assert.equal(/(files|tasks)-scrim/.test(html), false, "nothing dims the terminal");
});

// The panels are siblings of `main` INSIDE the content row: that is what
// makes their clamp resolve against the space right of the sidebar, and
// what keeps the terminal beside them rather than under them.
test("both panels sit in the content row, after the main column", () => {
  const html = workspaceLayout();
  const content = html.indexOf('data-region="content"');
  const main = html.indexOf("<main");
  const files = html.indexOf('data-region="files"');
  const tasks = html.indexOf('data-region="tasks"');
  const contentEnd = html.indexOf('data-region="modals"');
  assert.ok(content < main && main < files && files < tasks && tasks < contentEnd, html);
});

// A closed panel remains laid out as a full surface so the open state can
// reveal it in place; `hidden` would drop it from the flex row entirely,
// and the visibility toggle keeps a collapsed sidebar out of the tab
// order.
test("the docked panel surfaces stay in the flex row while closed", () => {
  const html = workspaceLayout();
  for (const region of ["files", "tasks"] as const) {
    assert.equal(aside(html, region).includes('class="hidden'), false, `${region} uses visibility`);
  }
});

test("the shared titlebar exposes one safe-area-aware layout hook", () => {
  assert.match(workspaceLayout(), /data-workspace-titlebar/);
});

test("the version action starts hidden and never claims an automatic reload", () => {
  const html = workspaceLayout();
  assert.match(html, /data-version-reload[^>]*\bhidden/);
  assert.ok(html.includes("New version available"));
});
