// The sidebar poll must not tear down a rail it did not change. Every poll
// tick runs refresh-then-render; when the model still describes the rail on
// screen, the render has to leave the container's nodes alone, because
// replacing them restarts the streaming spinners' CSS animations in unison,
// drops list focus, and discards uncommitted rename text. This probe drives
// the real poll loop against the fixture preview and holds node references
// across the ticks: a row that was rebuilt is a different node, however
// identical its markup. The complement leg pins the other side — a genuine
// model change must still repaint, or the guard is a wall rather than a
// diff.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";
import { eventually } from "./eventually.mjs";

const dist = resolve(import.meta.dirname, "..", "dist");
if (!existsSync(join(dist, "preview.html"))) {
  throw new Error("dist/preview.html is missing — run pnpm build first (embed builds drop the preview)");
}

const mime = { ".css": "text/css", ".html": "text/html", ".js": "text/javascript", ".svg": "image/svg+xml", ".png": "image/png", ".woff2": "font/woff2" };
const server = createServer(async (req, res) => {
  const name = new URL(req.url, "http://x").pathname.slice(1);
  const path = join(dist, name);
  if (!path.startsWith(dist) || !existsSync(path)) {
    res.writeHead(404);
    res.end("not found");
    return;
  }
  res.writeHead(200, { "Content-Type": mime[extname(path)] ?? "application/octet-stream" });
  res.end(await readFile(path));
});

await new Promise((resolveServer) => server.listen(0, "127.0.0.1", resolveServer));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  ({ browser } = await getBrowser());
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  // 250ms ticks: several poll cycles fit inside a short wall-clock window,
  // well before any fixture age bucket (< 1m rolling to 1m) would change the
  // markup for a legitimate reason and force the repaint this probe guards.
  await page.goto(`${origin}/preview.html?sidebar-poll=250`, { waitUntil: "networkidle" });
  await page.waitForSelector("html[data-preview-state]");
  assert.equal(await page.locator("html[data-preview-error]").count(), 0,
    "the preview reached its capture state but also carries an error marker");

  const capture = () => page.evaluate(() => {
    const row = document.querySelector('[data-session="sess-2"]');
    const spinner = row?.querySelector('[aria-label="output streaming"]') ?? null;
    const bystander = document.querySelector('[data-session="sess-9"]');
    const ageRow = document.querySelector('[data-session="sess-1"]');
    window.__probeRail = { row, spinner, bystander, ageRow };
    return {
      row: row !== null,
      spinner: spinner !== null,
      bystander: bystander !== null,
      ageRow: ageRow !== null,
      ageText: ageRow?.querySelector("[data-session-meta]")?.textContent ?? null,
    };
  });

  const identity = () => page.evaluate(() => {
    const { row, spinner, bystander } = window.__probeRail;
    return {
      rowKept: document.querySelector('[data-session="sess-2"]') === row,
      rowConnected: row.isConnected,
      spinnerKept: row.querySelector('[aria-label="output streaming"]') === spinner,
      spinnerConnected: spinner.isConnected,
      bystanderKept: document.querySelector('[data-session="sess-9"]') === bystander,
    };
  });

  // The fixture primes its spinner AFTER the settling render (preview.ts
  // calls setStreaming once the groups load), so the FIRST poll tick
  // legitimately repaints — the priming changed the streaming set the
  // markup embeds. Capturing before that tick would hold nodes the rail
  // is supposed to replace. Wait one full tick past the settle so the
  // references this probe holds are the nodes the idle ticks must keep.
  await page.waitForTimeout(600);
  // The fixture primes the codex row's streaming spinner, so the node this
  // leg holds is the exact element whose animation restart is the report.
  const seen = await capture();
  assert.ok(seen.row && seen.bystander, "the fixture rail did not render its session rows");
  assert.ok(seen.spinner, "the fixture did not prime the codex row's streaming spinner");

  await page.waitForTimeout(1500);
  const idle = await identity();
  assert.equal(idle.rowKept, true,
    "a poll tick that changed nothing replaced the session row's node");
  assert.equal(idle.rowConnected, true,
    "a poll tick that changed nothing detached the session row");
  assert.equal(idle.spinnerKept, true,
    "a poll tick that changed nothing replaced the streaming spinner's node — its animation restarted");
  assert.equal(idle.spinnerConnected, true,
    "a poll tick that changed nothing detached the streaming spinner");
  assert.equal(idle.bystanderKept, true,
    "a poll tick that changed nothing replaced a row nobody touched");
  console.log("ok: idle poll ticks leave the rail's nodes alone");

  // The complement: selecting a session is a real model change, and the
  // render that follows must still rebuild the rail. The bystander row is
  // not the row that was clicked, so its replacement proves the whole rail
  // repainted rather than one row swapping.
  await page.locator('[data-session="sess-2"]').click();
  await page.waitForTimeout(600);
  const changed = await page.evaluate(() => {
    const { row, bystander } = window.__probeRail;
    const selected = document.querySelector('[data-session][aria-selected="true"]');
    return {
      selected: selected?.getAttribute("data-session") ?? null,
      rowReplaced: document.querySelector('[data-session="sess-2"]') !== row,
      bystanderReplaced: document.querySelector('[data-session="sess-9"]') !== bystander,
      spinnerRendered: document.querySelector('[data-session="sess-2"] [aria-label="output streaming"]') !== null,
    };
  });
  assert.equal(changed.selected, "sess-2",
    "clicking the codex row did not select it — the later assertions would not be about a real change");
  assert.equal(changed.rowReplaced, true,
    "a genuine model change left the clicked row's node in place — the guard swallowed a repaint");
  assert.equal(changed.bystanderReplaced, true,
    "a genuine model change left an untouched row's node in place — the rail never repainted");
  assert.equal(changed.spinnerRendered, true,
    "the repaint lost the spinner the streaming state still calls for");
  console.log("ok: a genuine model change still repaints the rail");

  // And the guard re-arms after that repaint: the next idle ticks again
  // preserve the new nodes.
  await capture();
  await page.waitForTimeout(1500);
  const resettled = await identity();
  assert.equal(resettled.rowKept && resettled.spinnerKept && resettled.bystanderKept, true,
    "after a repaint, idle poll ticks went back to replacing the rail's nodes");
  console.log("ok: idle ticks preserve the rail again after a repaint");

  // A change the poll itself discovers — not one a user gesture
  // renders immediately. The fixture stamps sess-1's last output 42s
  // before load, so its age tick reads < 1m until the page is ~18s old
  // and 1m after; the poll ticks over that boundary and its freshly
  // built markup no longer equals the cached bytes, so the rail must
  // repaint. The node identity is the verdict, not the visible age: the
  // 30s out-of-band meta tick can repaint the AGE without replacing any
  // node, so only replacement proves the render path let the change
  // through.
  const settled = await capture();
  assert.equal(settled.ageText, "< 1m",
    `sess-1's age tick should still read < 1m at capture (got ${JSON.stringify(settled.ageText)}); `
      + "if formatSince's bucketing changed, repoint this leg at a row whose bucket still crosses");
  await eventually(
    () => page.evaluate(() => {
      const { ageRow } = window.__probeRail;
      const current = document.querySelector('[data-session="sess-1"]');
      return current !== null && current !== ageRow;
    }),
    (replaced) => replaced,
    "the age bucket crossed but the poll never replaced sess-1's row — the guard holds changes the model did make",
    45_000,
  );
  const rolled = await page.evaluate(() => ({
    ageText: document.querySelector('[data-session="sess-1"] [data-session-meta]')?.textContent ?? null,
  }));
  assert.equal(rolled.ageText, "1m",
    `the row that replaced sess-1 should carry the crossed age bucket (got ${JSON.stringify(rolled.ageText)})`);
  console.log("ok: a change the poll itself discovers still repaints the rail");

  assert.deepEqual(pageErrors, [], "no page errors");
  console.log("the sidebar poll preserves an unchanged rail and repaints a changed one: PASS");
  await context.close();
} finally {
  await browser?.close();
  server.close();
}
