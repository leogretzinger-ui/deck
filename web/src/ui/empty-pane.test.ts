// The empty pane's shape: one anatomy for every
// state, on the page surface, in the interface voice. These pin the
// copy's sentence case, the small-caps treatment (font-variant-caps,
// never text-transform), the absence of mono, the ≤44ch body, the
// optical lift, and the one primary action — and that an unread
// registry renders as its own state, never as a known-empty database
// that offers onboarding.
import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import {
  emptyPaneHtml,
  emptyTitlebarHtml,
} from "./empty-pane.ts";

test("no-session renders the shared anatomy with the project interpolated", () => {
  const html = emptyPaneHtml({ kind: "no-session", projectName: "field-notes" });
  assert.ok(html.includes('data-empty-pane="no-session"'), "the pane carries its state hook");
  assert.ok(html.includes(">No session open</h2>"), "the title is the sentence-case source string");
  assert.ok(
    html.includes("Pick a session from the side panel, or start a new one in <em>field-notes</em>."),
    "the body interpolates the project so the sentence and the button agree",
  );
  assert.ok(html.includes('data-empty-action'), "exactly one primary action");
  assert.ok(html.includes(">New session</button>"), "the action names what it does");
});

test("no-project renders the same anatomy with its own copy", () => {
  const html = emptyPaneHtml({ kind: "no-project", projectName: null });
  assert.ok(html.includes('data-empty-pane="no-project"'), "the pane carries its state hook");
  assert.ok(html.includes(">No projects yet</h2>"));
  assert.ok(
    html.includes("Point Deck at a directory, an existing repository, or a repository to clone."),
  );
  assert.ok(html.includes(">Add project</button>"));
});

test("small caps is a treatment, not copy casing", async () => {
  for (const kind of ["no-session", "no-project"] as const) {
    const html = emptyPaneHtml({
      kind,
      projectName: kind === "no-session" ? "field-notes" : null,
    });
    const title = /<h2 data-empty-title class="([^"]*)"/.exec(html)![1]!;
    assert.ok(
      title.includes("font-title-caps"),
      `${kind}: the title carries the small-caps utility`,
    );
    assert.ok(
      !/\buppercase\b/.test(title),
      `${kind}: the title never uses text-transform casing`,
    );
    // The utility must be the real treatment: reading textTransform tells
    // you nothing about all-small-caps, so the stylesheet rule itself is
    // pinned here.
    const css = await readFile(new URL("../styles/app.css", import.meta.url), "utf8");
    const utility = /@utility font-title-caps \{[^}]*\}/.exec(css)?.[0] ?? "";
    assert.match(utility, /font-variant-caps:\s*var\(--type-caps\)/);
    assert.match(css, /--type-caps:\s*all-small-caps/);
  }
});

test("nothing in the empty pane is mono", () => {
  for (const kind of ["no-session", "no-project"] as const) {
    const html = emptyPaneHtml({
      kind,
      projectName: kind === "no-session" ? "field-notes" : null,
    });
    assert.ok(
      !html.includes("font-mono") && !html.includes("--font-mono"),
      `${kind}: mono is the machine voice and never marks the app's own instruction`,
    );
    assert.ok(
      html.includes("text-title-3"),
      `${kind}: the title is title-3, per the anatomy`,
    );
  }
});

test("the body is capped at 44ch and sits on the interface surface", () => {
  const html = emptyPaneHtml({ kind: "no-session", projectName: "field-notes" });
  assert.ok(
    html.includes("max-w-[44ch]"),
    "the body line is measure-capped at 44ch",
  );
  assert.ok(
    html.includes("pb-[7%]"),
    "the composition is lifted optically, not geometrically centred",
  );
});

test("the states carry their own icons", () => {
  const session = emptyPaneHtml({ kind: "no-session", projectName: "p" });
  const project = emptyPaneHtml({ kind: "no-project", projectName: null });
  // The layers icon's stacked-planes path vs the folder's single outline.
  assert.ok(session.includes("M12.83 2.18"), "no-session leads with the layers icon");
  assert.ok(project.includes("M20 20a2 2 0 0 0 2-2V8"), "no-project leads with the folder icon");
});

test("a failed read is its own state, never a known-empty one", () => {
  const html = emptyPaneHtml({ kind: "error", projectName: null, error: "project registry unavailable" });
  assert.ok(html.includes('data-empty-pane="error"'), "the pane carries its state hook");
  assert.ok(html.includes('role="alert"'), "a failure is announced");
  assert.ok(html.includes(">Can't reach the runtime</h2>"));
  assert.ok(!html.includes("No projects yet"), "a failed read must never borrow the no-project copy");
  assert.ok(!html.includes("Add project"), "nor its onboarding action");
  assert.ok(
    html.includes("Your sessions are still on the server."),
    "the body reassures before it explains",
  );
  assert.ok(html.includes("Retrying every 10 seconds."), "and says the page keeps trying");
  assert.ok(
    html.includes('data-empty-detail class="max-w-[44ch] text-caption text-error">project registry unavailable</p>'),
    "the server's own message trails as a caption, never as the page's explanation",
  );
  assert.ok(html.includes(">Retry now</button>"), "the one action retries the read");
  assert.ok(html.includes("M12 9v4"), "error leads with the triangle-alert icon");
  assert.ok(html.includes('class="text-warning"'), "in the warning tone");
});

test("the error state with a cached rail says whose rows those are", () => {
  const html = emptyPaneHtml({ kind: "error", projectName: null, cached: true });
  assert.ok(html.includes("The list on the left is from your last visit."));
  assert.ok(!html.includes("data-empty-detail"), "no message, no caption");
});

test("the loading pane is a spinner and a line, not an empty state", () => {
  const html = emptyPaneHtml({ kind: "loading", projectName: null });
  assert.ok(html.includes('data-empty-pane="loading"'));
  assert.ok(html.includes('data-empty-phase="reconnect"'), "the first phase by default");
  assert.ok(html.includes('role="status"') && html.includes('aria-busy="true"'));
  assert.ok(html.includes("animate-spinner"), "a spinner leads");
  assert.ok(html.includes("Reconnecting to your sessions…"));
  assert.ok(!html.includes("data-empty-title"), "no title: nothing has been found out yet");
  assert.ok(!html.includes("data-empty-action"), "no action: nothing to act on yet");
  assert.ok(!html.includes("No projects yet") && !html.includes("No session open"), "never an empty state's copy");
  assert.ok(!html.includes("font-mono"), "the app's own voice, not the machine's");
  assert.ok(html.includes("pb-[7%]"), "lifted like the settled states so the swap lands in place");
});

test("the loading pane names the session it is reattaching to", () => {
  const html = emptyPaneHtml({ kind: "loading", projectName: null, session: "Fix <PRD> parser" });
  assert.ok(html.includes("Reattaching to <em>Fix &lt;PRD&gt; parser</em>…"), "the remembered row, escaped");
});

test("the slow phase says the wait is the runtime's and nothing has been closed", () => {
  const html = emptyPaneHtml({ kind: "loading", projectName: null, phase: "slow" });
  assert.ok(html.includes('data-empty-phase="slow"'));
  assert.ok(html.includes("Still reaching the runtime"));
  assert.ok(html.includes("Nothing has been closed."));
  const cached = emptyPaneHtml({ kind: "loading", projectName: null, phase: "slow", cached: true });
  assert.ok(cached.includes("These are your sessions as of your last visit."));
});

test("the loading and error titlebars shed identity and panels; only the account menu stays", () => {
  for (const kind of ["loading", "error"] as const) {
    const html = emptyTitlebarHtml("live", { kind, project: null });
    assert.ok(!html.includes("data-terminal-identity"), `${kind}: no identity before a project is readable`);
    assert.ok(!html.includes("data-live-files") && !html.includes("data-live-tasks"), `${kind}: no panel triggers`);
    assert.ok(html.includes("data-live-more"), `${kind}: the account menu stays`);
  }
});


test("the no-session titlebar keeps the session chrome with the project in the identity slot", () => {
  const html = emptyTitlebarHtml("live", {
    kind: "no-session",
    project: { name: "field-notes", tint: null },
  });
  assert.ok(html.includes("data-terminal-identity"));
  assert.ok(html.includes(">field-notes</span>"), "the header names the project, not the absent session");
  assert.ok(html.includes("data-live-files"));
  assert.ok(html.includes("data-live-tasks"));
  assert.ok(html.includes("data-live-sessions"), "Sessions opens the sidebar on mobile");
  assert.ok(html.includes("data-live-more"));
});

test("the no-project titlebar sheds identity and panels; only the account menu stays", () => {
  const html = emptyTitlebarHtml("titlebar", {
    kind: "no-project",
    project: null,
  });
  assert.ok(!html.includes("data-terminal-identity"), "no project identity without a project");
  assert.ok(!html.includes("data-titlebar-files"), "Files has nothing to read");
  assert.ok(!html.includes("data-titlebar-tasks"), "Tasks has nothing to read");
  assert.ok(!html.includes("data-titlebar-sessions"), "Sessions has nothing to open");
  assert.ok(html.includes("data-titlebar-more"), "the account menu stays");
});

test("project names are interpolated escaped", () => {
  const html = emptyPaneHtml({ kind: "no-session", projectName: `<b>evil & co</b>` });
  assert.ok(!html.includes("<b>evil"), "the project name is escaped into the body");
  assert.ok(html.includes("&lt;b&gt;evil &amp; co&lt;/b&gt;"));
});

test("workspace actions expose panels beside a distinct settings control", async () => {
  const { titlebarRightHtml } = await import("./empty-pane.ts");
  for (const surface of ["live", "titlebar"] as const) {
    const html = titlebarRightHtml(surface, { panels: true, sessions: true, chats: { unseen: true } });
    assert.match(html, /aria-label="Settings"/);
    assert.match(html, /aria-label="Panels" aria-haspopup="menu" aria-expanded="false" data-unseen="true"/);
    assert.match(html, /aria-label="Sessions"/);
    assert.ok(html.indexOf(`data-${surface}-sessions`) < html.indexOf(`data-${surface}-panels`));
    assert.ok(html.indexOf(`data-${surface}-panels`) < html.indexOf(`data-${surface}-more`));
    for (const panel of ["files", "tasks", "chats"]) assert.ok(html.includes(`data-${surface}-${panel}`));
    const empty = titlebarRightHtml(surface, { panels: false });
    assert.ok(!empty.includes(`data-${surface}-panels`));
    assert.match(empty, /aria-label="Settings"/);
  }
});
