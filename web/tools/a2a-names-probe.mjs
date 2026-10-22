// Browser leg of the isolated a2a names fixture. The Go test supplies its
// live backend origin and session IDs; this drives the real sidebar rename.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { browser as getBrowser } from "./browser.mjs";

const readyPath = process.argv[2];
if (readyPath === undefined) {
  throw new Error("usage: node tools/a2a-names-probe.mjs <readyfile.json>");
}
const ready = JSON.parse(await readFile(readyPath, "utf8"));
for (const key of ["origin", "project_id", "session_a", "session_b", "label"]) {
  if (typeof ready[key] !== "string" || ready[key] === "") {
    throw new Error(`the readyfile is missing ${key}`);
  }
}

let browser;
try {
  ({ browser } = await getBrowser());
  const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
  const page = await context.newPage();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));

  await page.goto(ready.origin, { waitUntil: "domcontentloaded" });

  // A first-run browser meets the docked tutorial; it must not stand between
  // the user and the sidebar, and closing it is the ordinary gesture.
  const tutorialClose = page.locator('[data-tutorial-action="close"]');
  if (await tutorialClose.count() > 0) {
    await tutorialClose.first().click({ timeout: 10_000 }).catch(() => {});
  }

  // Both fixture sessions must be in the rail before anything is renamed.
  const rowA = page.locator(`[data-session="${ready.session_a}"]`);
  const rowB = page.locator(`[data-session="${ready.session_b}"]`);
  await rowA.waitFor({ state: "visible", timeout: 60_000 });
  await rowB.waitFor({ state: "visible", timeout: 10_000 });

  const renameViaMenu = async (row, label) => {
    await row.hover();
    const id = await row.getAttribute("data-session");
    await page.locator(`[data-session-menu="${id}"]`).click();
    await page.locator("[data-session-menu-rename]").click();
    const field = page.locator("[data-rename-input]");
    await field.waitFor({ state: "visible" });
    await field.fill(label);
    await field.press("Enter");
    await field.waitFor({ state: "detached", timeout: 30_000 });
  };

  await renameViaMenu(rowA, ready.label);

  // The row repaints from the server's truth: the label the user typed
  // is the row's name now, and the untouched sibling still renders its own.
  await page.waitForFunction(
    ({ id, label }) => {
      const row = document.querySelector(`[data-session="${id}"]`);
      return row !== null && row.textContent !== null && row.textContent.includes(label)
        && row.querySelector("[data-rename-input]") === null;
    },
    { id: ready.session_a, label: ready.label },
    { timeout: 30_000 },
  );
  const siblingText = await rowB.textContent();
  assert.ok(siblingText !== null && !siblingText.includes(ready.label),
    `the untouched sibling row must not carry the other session's label: ${siblingText}`);

  assert.deepEqual(pageErrors, [], "no page errors");
  console.log(`ok: the panel renamed session ${ready.session_a.slice(0, 8)} to "${ready.label}" and the row repainted`);
  console.log("the panel rename leg: PASS");
  await context.close();
} finally {
  await browser?.close();
}
