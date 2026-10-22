// The brand is not a control surface. Neither the expanded lockup nor the
// collapsed mark takes a hover wash or a hover tint, and collapsing is the only
// state either reports: the wordmark gives way to the evidence-coloured mark.
// Both remain keyboard-reachable, so keyboard focus must be visible — as
// the hover wash, never a ring (no outline, ever). The
// can actually see against the titlebar.
//
// Computed styles at rest and under a real pointer, because a hover rule that
// is merely absent from the markup can still arrive from a utility class.
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

  const styleOf = (sel, prop) => page.evaluate(([s, p]) => {
    const el = document.querySelector(s);
    if (el === null) throw new Error(`missing ${s}`);
    return getComputedStyle(el)[p];
  }, [sel, prop]);

  const transparent = (c) => c === "rgba(0, 0, 0, 0)" || c === "transparent";

  // --- expanded: the lockup ---
  const lockup = "[data-collapse]";
  await page.locator(lockup).waitFor();
  const lockupRest = { bg: await styleOf(lockup, "backgroundColor"), fg: await styleOf(lockup, "color") };
  assert.ok(transparent(lockupRest.bg), `the lockup rests transparent, got ${lockupRest.bg}`);
  await page.locator(lockup).hover();
  await page.waitForTimeout(150);
  const lockupHover = { bg: await styleOf(lockup, "backgroundColor"), fg: await styleOf(lockup, "color") };
  assert.ok(transparent(lockupHover.bg), `hovering the lockup painted ${lockupHover.bg}`);
  assert.equal(lockupHover.fg, lockupRest.fg, "hovering the lockup changed its colour");

  // Keyboard focus is the hover wash, never a ring: outline stays none and
  // the background takes the wash whenever the engine grants :focus-visible.
  await page.locator(lockup).focus();
  await page.evaluate(() => {
    const el = document.querySelector("[data-collapse]");
    el.dispatchEvent(new FocusEvent("focus"));
  });
  const lockupFocus = await page.evaluate(() => {
    const el = document.querySelector("[data-collapse]");
    el.focus();
    const cs = getComputedStyle(el);
    return { style: cs.outlineStyle, matched: el.matches(":focus-visible"), bg: cs.backgroundColor };
  });
  assert.equal(lockupFocus.style, "none", "keyboard focus never paints an outline");
  if (lockupFocus.matched) {
    assert.ok(!transparent(lockupFocus.bg),
      "the keyboard-focused lockup takes the hover wash, not nothing");
  }

  // --- collapsed: the mark ---
  await page.locator(lockup).click();
  const markButton = '[data-region="sidebar-expand"]';
  await page.locator(markButton).waitFor({ state: "visible" });
  const markRest = await styleOf(markButton, "backgroundColor");
  assert.ok(transparent(markRest), `the collapsed control rests transparent, got ${markRest}`);
  await page.locator(markButton).hover();
  await page.waitForTimeout(150);
  const markHover = await styleOf(markButton, "backgroundColor");
  assert.ok(transparent(markHover), `hovering the collapsed control painted ${markHover}`);

  // The mark is the evidence colour, and that swap is the state change.
  const markInk = await page.evaluate(() => {
    const el = document.querySelector('[data-region="sidebar-expand"] span');
    if (el === null) throw new Error("the collapsed mark is not rendered");
    return getComputedStyle(el).backgroundColor;
  });
  const evidence = await page.evaluate(() => {
    const probe = document.createElement("span");
    probe.style.backgroundColor = "var(--color-evidence)";
    document.body.appendChild(probe);
    const c = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return c;
  });
  assert.equal(markInk, evidence, `the collapsed mark is ${markInk}, not the evidence colour ${evidence}`);

  // The collapsed state's focusable control is the expand button — the
  // lockup's own button is visibility:hidden here and would pass any
  // assertion vacuously.
  const markFocus = await page.evaluate(() => {
    const el = document.querySelector('[data-region="sidebar-expand"] button')
      ?? document.querySelector('[data-region="sidebar-expand"]');
    if (el === null || getComputedStyle(el).visibility === "hidden") {
      throw new Error("the collapsed expand control is not focusable");
    }
    el.focus();
    const cs = getComputedStyle(el);
    return {
      style: cs.outlineStyle,
      matched: el.matches(":focus-visible"),
      focused: document.activeElement === el,
      bg: cs.backgroundColor,
    };
  });
  assert.equal(markFocus.focused, true, "the expand control did not take focus");
  assert.equal(markFocus.style, "none", "keyboard focus never paints an outline on the collapsed control");
  if (markFocus.matched) {
    assert.ok(!transparent(markFocus.bg),
      "the keyboard-focused collapsed control takes the hover wash");
  }

  assert.deepEqual(pageErrors, [], "no page errors");
  console.log("the brand mark stays flat, swaps to the evidence colour on collapse, and keeps ring-free keyboard focus on both controls: PASS");
  await context.close();
} finally {
  await browser?.close();
  server.close();
}
