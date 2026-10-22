// The accent caret marks the field taking keystrokes (comp desktop-r15). The
// comp declares it on four fields and leaves the rest default, so this checks
// both halves: a selector that reaches too far is as wrong as one that misses.
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

  const caretOf = (sel) => page.evaluate((s) => {
    const el = document.querySelector(s);
    if (el === null) return null;
    return getComputedStyle(el).caretColor;
  }, sel);

  const accent = await page.evaluate(() => {
    const probe = document.createElement("span");
    probe.style.color = "var(--color-accent)";
    document.body.appendChild(probe);
    const c = getComputedStyle(probe).color;
    probe.remove();
    return c;
  });

  // Positive: the provider filter is reachable from the preview's new-session
  // picker, and carries the accent.
  // The row's + rests hidden and reveals on hover; a hit test alone never moves
  // the pointer, so the probe performs the user's hover first.
  const plus = page.locator("[data-project-actions] [data-new-session]").first();
  await plus.locator("xpath=ancestor::li[1]").locator("[data-project-toggle]").hover();
  await plus.click();
  const filter = "[data-new-session-filter]";
  await page.locator(filter).waitFor();
  // The dialog is the only place the target project is named: the Projects
  // menu row that opens it carries no caption. Without this line the user
  // chooses a harness with nothing saying where the session lands.
  const desc = page.locator("[data-new-session-modal] .dialog-desc");
  assert.equal(await desc.count(), 1, "the New session dialog stopped naming its target project");
  const firstProject = await plus.locator("xpath=ancestor::li[1]").getAttribute("data-project");
  const firstName = (await page.locator(`[data-project="${firstProject}"] [data-project-name]`).first().innerText()).trim();
  assert.equal((await desc.innerText()).trim(), `in ${firstName}`,
    `the dialog names ${JSON.stringify(await desc.innerText())} for project ${JSON.stringify(firstName)}`);
  assert.equal(await caretOf(filter), accent,
    `the provider filter's caret is ${await caretOf(filter)}, not the accent ${accent}`);

  // A project row that is NOT the context project is the case that catches a
  // dialog naming the wrong target: the description must come from the id the
  // launch uses, not from a context-else-first resolution that agrees only for
  // the first row. Opening the first project alone cannot see the difference.
  await page.keyboard.press("Escape");
  await page.locator("[data-new-session-modal]").waitFor({ state: "detached" });
  const otherPlus = page.locator("[data-project-actions] [data-new-session]").nth(1);
  // Fail loudly rather than skipping: a fixture that shrinks to one project
  // would silently discard the only coverage of the clicked-row target.
  assert.ok(await otherPlus.count() > 0,
    "the fixture has no second project, so the non-first-row target is unguarded");
  {
    const otherLi = otherPlus.locator("xpath=ancestor::li[1]");
    const otherName = (await otherLi.locator("[data-project-name]").first().innerText()).trim();
    await otherLi.locator("[data-project-toggle]").hover();
    await otherPlus.click();
    await page.locator(filter).waitFor();
    const otherDesc = page.locator("[data-new-session-modal] .dialog-desc");
    assert.equal(await otherDesc.count(), 1, `the dialog named no target for ${otherName}`);
    assert.equal((await otherDesc.innerText()).trim(), `in ${otherName}`,
      `opened from ${JSON.stringify(otherName)} the dialog names ${JSON.stringify(await otherDesc.innerText())}`);
    await page.keyboard.press("Escape");
    await page.locator("[data-new-session-modal]").waitFor({ state: "detached" });
    await plus.locator("xpath=ancestor::li[1]").locator("[data-project-toggle]").hover();
    await plus.click();
    await page.locator(filter).waitFor();
  }
  // The field is focused on open and wears the focused border the comp
  // draws: the hairline, quieter than rest, with the accent caret carrying
  // the focus. A field resting at the strong border has lost that rule.
  //
  // .input transitions border-color over 120ms, so an immediate read returns
  // the resting value whether or not focus has won. Confirm the field
  // actually owns focus, then let the transition settle before reading.
  assert.ok(
    await page.evaluate(() => document.activeElement?.matches("[data-new-session-filter]") === true),
    "the filter does not own focus, so its border proves nothing",
  );
  await page.waitForTimeout(300);
  const [filterBorder, restingBorder, hairlineBorder] = await page.evaluate(() => {
    const el = document.querySelector("[data-new-session-filter]");
    const probe = document.createElement("input");
    probe.className = "input";
    // .input transitions border-color, so the sample box gets none: a read
    // right after the token is set must be the token, not a frame of the
    // transition towards it.
    probe.style.transition = "none";
    el.parentElement.appendChild(probe);
    const resting = getComputedStyle(probe).borderTopColor;
    // The token resolves through the same box so the two colours compare
    // in one colour space.
    probe.style.borderColor = "var(--color-border)";
    const hairline = getComputedStyle(probe).borderTopColor;
    probe.remove();
    return [getComputedStyle(el).borderTopColor, resting, hairline];
  });
  assert.notEqual(hairlineBorder, restingBorder, "the hairline and resting borders are one colour, so the read proves nothing");
  assert.equal(filterBorder, hairlineBorder,
    `the autofocused filter shows (${filterBorder}) instead of the focused hairline (${hairlineBorder})`);
  await page.keyboard.press("Escape");

  // Negative: the task board's search shares .input with the filter, and the
  // comp supplies no declaration for it. A rule hung on .input would colour it.
  const search = "[data-tm-search]";
  if (await page.locator(search).count() > 0) {
    const c = await caretOf(search);
    assert.notEqual(c, accent,
      "the task search took the accent caret; the comp leaves it default");
    // Without a caret to speak for it, this field's focus is its border:
    // the quiet treatment must not reach a field that has no other signal.
    await page.locator(search).focus();
    await page.waitForTimeout(300);
    const [searchBorder, strongBorder] = await page.evaluate(() => {
      const el = document.querySelector("[data-tm-search]");
      const probe = document.createElement("input");
      probe.className = "input";
      probe.style.transition = "none";
      el.parentElement.appendChild(probe);
      probe.style.borderColor = "var(--color-border-focus)";
      const strong = getComputedStyle(probe).borderTopColor;
      probe.remove();
      return [getComputedStyle(el).borderTopColor, strong];
    });
    assert.equal(searchBorder, strongBorder,
      `the focused task search shows (${searchBorder}) instead of the strong focus border (${strongBorder})`);
  }

  // Negative: the chromeless rename keeps the default caret, as the comp draws
  // it — four neighbouring fields are coloured and this one deliberately is not.
  const row = page.locator("[data-project-toggle]").first();
  const pid = await row.getAttribute("data-project-toggle");
  await row.hover();
  await page.locator(`[data-project-menu="${pid}"]`).click();
  await page.locator("[data-project-menu-rename]").click();
  const rename = "[data-rename-project-input]";
  await page.locator(rename).waitFor();
  const renameCaret = await caretOf(rename);
  assert.notEqual(renameCaret, accent,
    "the chromeless rename took the accent caret; the comp leaves it default");
  await page.keyboard.press("Escape");

  assert.deepEqual(pageErrors, [], "no page errors");
  console.log("the accent caret reaches the comp's fields and no others: PASS");
  await context.close();
} finally {
  await browser?.close();
  server.close();
}
