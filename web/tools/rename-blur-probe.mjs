// Leaving an inline project rename must end it, and the click that leaves it
// must also action what it landed on. Repainting during the blur replaces the
// sidebar between mousedown and mouseup, so the click never completes on its
// target and the user pays for it twice.
//
// A separate probe because shortcuts-probe.mjs is one ordered session whose
// legs inherit each other's rail state, and this one has to collapse and
// rename to set up.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { browser as getBrowser } from "./browser.mjs";

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
  await page.goto(`${origin}/preview.html`, { waitUntil: "networkidle" });
  await page.waitForSelector("html[data-preview-state]");

  const rows = page.locator("[data-project-toggle]");
  assert.ok(await rows.count() >= 2, "the preview needs a second project to click onto");
  const first = rows.nth(0);
  const second = rows.nth(1);
  const secondID = await second.getAttribute("data-project-toggle");
  const field = page.locator("[data-rename-project-input]");
  const expandedOf = (pid) => page.evaluate(
    (id) => document.querySelector(`[data-project-toggle="${id}"]`)?.getAttribute("aria-expanded"),
    pid,
  );

  // The preview harness does not bind the rename chord, so the edit is opened
  // the way a user can always reach it: the row's own menu.
  const openRename = async (row) => {
    const id = await row.getAttribute("data-project-toggle");
    await row.hover();
    await page.locator(`[data-project-menu="${id}"]`).click();
    await page.locator("[data-project-menu-rename]").click();
    await field.waitFor();
  };

  // One gesture onto another project's chevron: it must both end the rename
  // and toggle that row. A repaint during the blur would replace the target
  // between mousedown and mouseup and the click would action nothing.
  await openRename(first);
  const before = await expandedOf(secondID);
  // By id, not by index: the rename re-render can reorder the rows, and an
  // index locator would click a different project than the one measured.
  await page.locator(`[data-project-toggle="${secondID}"]`).click();
  await page.waitForTimeout(600);
  assert.notEqual(await expandedOf(secondID), before,
    "the click that left the rename did not toggle the row it landed on");
  assert.equal(await field.count(), 0, "the rename field survived the click that left it");
  console.log("ok: the click that leaves a rename also actions its target");

  // The same guarantee when the name actually changed. A rename that resolves
  // in the same tick — the fixture, or a warm cache — must not repaint before
  // the click it was caused by has landed.
  await openRename(first);
  await field.fill("renamed-by-probe");
  const beforeEdited = await expandedOf(secondID);
  await page.locator(`[data-project-toggle="${secondID}"]`).click();
  await page.waitForTimeout(600);
  assert.notEqual(await expandedOf(secondID), beforeEdited,
    "after an edited rename, the click that left the field did not toggle the row it landed on");
  assert.equal(await field.count(), 0, "the edited rename field survived the click that left it");
  // Commitment is not asserted here: the model refreshes from the preview
  // fixture, which has no memory of the edit, so the row reads its fixture name
  // either way. What this leg exists to pin is the interaction — an edited
  // rename resolves in the same tick and must still not repaint before the
  // click it was caused by has landed. The request itself is covered by the
  // shortcuts probe's exactly-once guard.
  console.log("ok: an edited rename still leaves the click its target");

  // And it ends when the click lands on nothing in particular.
  await openRename(first);
  await page.locator('[data-region="terminal"]').click({ position: { x: 30, y: 30 } });
  await field.waitFor({ state: "detached" });
  console.log("ok: clicking away ends the rename");

  assert.deepEqual(pageErrors, [], "no page errors");
  console.log("the inline project rename ends on blur and never costs a second click: PASS");
  await context.close();
} finally {
  await browser?.close();
  server.close();
}
