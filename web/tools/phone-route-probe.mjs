// The phone draws no centred dialogs. A screen the comp gives its own header,
// body and action bar is a route: it takes the frame, paints the app's own
// surface, and dims nothing, because the workspace it came from is gone rather
// than obscured (comp mobile-r8 screens 08 to 11). The same dialog at desktop
// width must still be the card it has always been — the route is a phone
// presentation, not a redesign.
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

await new Promise((listening) => server.listen(0, "127.0.0.1", listening));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
try {
  ({ browser } = await getBrowser());

  const read = async (page) => page.evaluate(() => {
    const dialog = document.querySelector("[data-theme-dialog]");
    const scrim = document.querySelector("[data-modal-scrim]");
    const host = document.querySelector("[data-phone-frame] #app") ?? document.querySelector("#app");
    const box = (el) => {
      if (el === null) return null;
      const r = el.getBoundingClientRect();
      return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
    };
    const surface = getComputedStyle(document.documentElement).getPropertyValue("--color-surface").trim();
    const probe = document.createElement("span");
    probe.style.backgroundColor = surface;
    document.body.appendChild(probe);
    const surfaceRGB = getComputedStyle(probe).backgroundColor;
    probe.remove();
    return {
      dialog: box(dialog),
      host: box(host),
      ground: dialog === null ? null : getComputedStyle(dialog).backgroundColor,
      scrimGround: scrim === null ? null : getComputedStyle(scrim).backgroundColor,
      radius: dialog === null ? null : getComputedStyle(dialog).borderTopLeftRadius,
      surfaceRGB,
      footBottom: (() => {
        const f = document.querySelector("[data-theme-dialog] .dialog-foot");
        return f === null ? null : Math.round(f.getBoundingClientRect().bottom);
      })(),
    };
  });

  // Phone: the route fills the app area, on the app's own ground.
  {
    const context = await browser.newContext({ viewport: { width: 393, height: 864 } });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/preview.html?frame=phone&dialog=custom-theme`, { waitUntil: "networkidle" });
    await page.waitForSelector("[data-theme-dialog]");
    const seen = await read(page);

    assert.deepEqual(seen.dialog, seen.host,
      `the route does not fill the app area: ${JSON.stringify(seen)}`);
    assert.equal(seen.ground, seen.surfaceRGB,
      `the route paints ${seen.ground}, not the app surface ${seen.surfaceRGB}`);
    assert.equal(seen.scrimGround, "rgba(0, 0, 0, 0)",
      `the route dims a workspace that is not there: ${seen.scrimGround}`);
    assert.equal(seen.radius, "0px", `the route keeps a card's corner: ${seen.radius}`);
    assert.equal(seen.footBottom, seen.host.y + seen.host.h,
      `the route's actions do not sit on the frame's bottom edge: ${JSON.stringify(seen)}`);
    assert.deepEqual(errors, [], "the route threw");
    await context.close();
  }

  // Desktop: unchanged. A phone presentation that leaks upward would take the
  // workspace away from every user, not just the one holding a phone.
  {
    const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const page = await context.newPage();
    await page.goto(`${origin}/preview.html?dialog=custom-theme`, { waitUntil: "networkidle" });
    await page.waitForSelector("[data-theme-dialog]");
    const seen = await read(page);
    assert.ok(seen.dialog.w < 900,
      `the desktop dialog is no longer a card: ${JSON.stringify(seen.dialog)}`);
    assert.notEqual(seen.radius, "0px", "the desktop dialog lost its corner radius");
    assert.notEqual(seen.scrimGround, "rgba(0, 0, 0, 0)", "the desktop dialog lost its scrim");
    await context.close();
  }

  // The Open project dialog is the same route (comp mobile-r8 screen 08),
  // with chrome of its own to hold: the caption under the header, said once
  // and ended in the rule; the foot's Cancel gone, because the header's X is
  // the way out; the primary action across the width. Read at both narrow
  // scopes — the real media query and the framed [data-viewport="narrow"]
  // mirror — because each has its own copy of the rules to lose.
  const readProject = async (page) => page.evaluate(() => {
    const dialog = document.querySelector("[data-project-form]");
    const host = document.querySelector("[data-phone-frame] #app") ?? document.querySelector("#app");
    const box = (el) => {
      if (el === null) return null;
      const r = el.getBoundingClientRect();
      return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
    };
    const surface = getComputedStyle(document.documentElement).getPropertyValue("--color-surface").trim();
    const probe = document.createElement("span");
    probe.style.backgroundColor = surface;
    document.body.appendChild(probe);
    const surfaceRGB = getComputedStyle(probe).backgroundColor;
    probe.remove();
    const caption = document.querySelector("[data-project-caption]");
    const footCancel = document.querySelector("[data-project-form] .dialog-foot [data-project-cancel]");
    const submit = document.querySelector("[data-project-submit]");
    const scrim = document.querySelector("[data-modal-scrim]");
    return {
      dialog: box(dialog),
      host: box(host),
      ground: dialog === null ? null : getComputedStyle(dialog).backgroundColor,
      radius: dialog === null ? null : getComputedStyle(dialog).borderTopLeftRadius,
      scrimGround: scrim === null ? null : getComputedStyle(scrim).backgroundColor,
      surfaceRGB,
      caption: caption === null ? null : {
        display: getComputedStyle(caption).display,
        text: caption.textContent.trim(),
        rule: getComputedStyle(caption).borderBottomWidth,
      },
      footCancelDisplay: footCancel === null ? null : getComputedStyle(footCancel).display,
      submitGrow: submit === null ? null : getComputedStyle(submit).flexGrow,
    };
  });
  for (const [label, viewport] of [
    ["real", { width: 393, height: 864 }],
    ["framed", { width: 1400, height: 900 }],
  ]) {
    const context = await browser.newContext({ viewport });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/preview.html?frame=phone&dialog=open-project`, { waitUntil: "networkidle" });
    await page.waitForSelector("[data-project-form]");
    const seen = await readProject(page);
    assert.deepEqual(seen.dialog, seen.host,
      `${label}: Open project does not fill the app area: ${JSON.stringify(seen)}`);
    assert.equal(seen.ground, seen.surfaceRGB,
      `${label}: Open project paints ${seen.ground}, not the app surface ${seen.surfaceRGB}`);
    assert.equal(seen.radius, "0px", `${label}: Open project keeps a card's corner: ${seen.radius}`);
    assert.equal(seen.scrimGround, "rgba(0, 0, 0, 0)",
      `${label}: Open project dims a workspace that is not there: ${seen.scrimGround}`);
    assert.equal(seen.caption?.display, "block", `${label}: the caption is not phone-visible: ${JSON.stringify(seen.caption)}`);
    assert.equal(seen.caption?.text, "Open project adds this folder to your sidebar",
      `${label}: the caption says: ${seen.caption?.text}`);
    assert.equal(seen.caption?.rule, "1px", `${label}: the caption does not end in the rule: ${seen.caption?.rule}`);
    assert.equal(seen.footCancelDisplay, "none",
      `${label}: the foot keeps its Cancel: ${seen.footCancelDisplay}`);
    assert.equal(seen.submitGrow, "1",
      `${label}: the primary action does not take the width: ${seen.submitGrow}`);
    // The header's X is the way out of the route.
    await page.click("[data-project-form] .dialog-head [data-project-cancel]");
    await page.waitForSelector("[data-project-form]", { state: "detached" });
    assert.deepEqual(errors, [], `${label}: the Open project route threw`);
    await context.close();
  }

  // Desktop: the card, whole. The caption stays in the hint bar's mouth, the
  // Cancel keeps its place, and the primary action keeps its size.
  {
    const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const page = await context.newPage();
    await page.goto(`${origin}/preview.html?dialog=open-project`, { waitUntil: "networkidle" });
    await page.waitForSelector("[data-project-form]");
    const seen = await readProject(page);
    assert.ok(seen.dialog.w < 900, `the desktop Open project is no longer a card: ${JSON.stringify(seen.dialog)}`);
    assert.notEqual(seen.radius, "0px", "the desktop Open project lost its corner radius");
    assert.notEqual(seen.scrimGround, "rgba(0, 0, 0, 0)", "the desktop Open project lost its scrim");
    assert.equal(seen.caption?.display, "none", `the caption leaked to the desktop: ${JSON.stringify(seen.caption)}`);
    assert.notEqual(seen.footCancelDisplay, "none", "the desktop foot lost its Cancel");
    assert.notEqual(seen.submitGrow, "1", `the desktop primary action stretched: ${seen.submitGrow}`);
    await context.close();
  }

  // The task detail is the second dialog on the route, reached through the
  // rendered card's own click handler. The same contract holds: the frame on
  // a phone, a card on the desktop.
  {
    const context = await browser.newContext({ viewport: { width: 393, height: 864 } });
    const page = await context.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${origin}/preview.html?frame=phone&panel=tasks&task=30.2`, { waitUntil: "networkidle" });
    await page.waitForSelector('[role="dialog"][aria-label="Task 30.2"]');
    const seen = await page.evaluate(() => {
      const dialog = document.querySelector('[role="dialog"][aria-label="Task 30.2"]');
      const host = document.querySelector("[data-phone-frame] #app");
      const box = (el) => {
        const r = el.getBoundingClientRect();
        return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) };
      };
      return {
        dialog: box(dialog),
        host: box(host),
        state: document.documentElement.dataset.previewState ?? null,
        radius: getComputedStyle(dialog).borderTopLeftRadius,
      };
    });
    assert.equal(seen.state, "task-detail", `the page marked ${seen.state}`);
    assert.deepEqual(seen.dialog, seen.host, `the detail does not fill the app area: ${JSON.stringify(seen)}`);
    assert.equal(seen.radius, "0px", `the detail keeps a card's corner: ${seen.radius}`);
    assert.deepEqual(errors, [], "the detail route threw");

    // The loading phase is the dialog's first paint — it renders synchronously
    // before the detail resolves, so a slow request shows it for as long as it
    // takes and a failed one leaves it up. It has to be the route from that
    // first frame: a card that snaps to full screen on resolution is a flash
    // the comp does not draw. Recorded at first appearance, because a settled
    // assertion only ever sees the resolved phase.
    {
      const bare = await context.newPage();
      await bare.goto(`${origin}/preview.html?frame=phone&panel=tasks`, { waitUntil: "networkidle" });
      await bare.waitForSelector("[data-tm-task]");
      await bare.evaluate(() => {
        window.__firstDetailPaint = null;
        new MutationObserver(() => {
          if (window.__firstDetailPaint !== null) return;
          const dialog = document.querySelector('[role="dialog"][aria-label^="Task "]');
          if (dialog === null) return;
          const host = document.querySelector("[data-phone-frame] #app");
          const r = dialog.getBoundingClientRect();
          const h = host.getBoundingClientRect();
          window.__firstDetailPaint = {
            fullscreen: dialog.hasAttribute("data-phone-fullscreen"),
            fills: Math.round(r.width) === Math.round(h.width) && Math.round(r.height) === Math.round(h.height),
            loading: dialog.textContent.includes("Loading task"),
          };
        }).observe(document.body, { subtree: true, childList: true });
      });
      await bare.click('[data-tm-task="30.2"]');
      await bare.waitForSelector('[role="dialog"][aria-label="Task 30.2"]');
      const first = await bare.evaluate(() => window.__firstDetailPaint);
      assert.ok(first !== null, "the detail dialog never appeared");
      assert.equal(first.fullscreen, true,
        `the detail's first paint is not the route: ${JSON.stringify(first)}`);
      assert.equal(first.fills, true,
        `the detail's first paint does not fill the app area: ${JSON.stringify(first)}`);
      await bare.close();
    }

    // An id that matches nothing stops the page rather than certifying the
    // list as the detail.
    const bad = await context.newPage();
    await bad.goto(`${origin}/preview.html?frame=phone&panel=tasks&task=nope`, { waitUntil: "networkidle" });
    const marks = await bad.evaluate(() => ({
      error: document.documentElement.dataset.previewError ?? null,
      state: document.documentElement.dataset.previewState ?? null,
    }));
    assert.match(marks.error ?? "", /task/, `an unresolved task did not stop the page: ${JSON.stringify(marks)}`);
    assert.equal(marks.state, null, `the page marked itself reached despite an unresolved task: ${JSON.stringify(marks)}`);
    await bad.close();
    await context.close();
  }

  {
    const context = await browser.newContext({ viewport: { width: 1400, height: 900 } });
    const page = await context.newPage();
    await page.goto(`${origin}/preview.html?panel=tasks&task=30.2`, { waitUntil: "networkidle" });
    await page.waitForSelector('[role="dialog"][aria-label="Task 30.2"]');
    const width = await page.evaluate(() =>
      Math.round(document.querySelector('[role="dialog"][aria-label="Task 30.2"]').getBoundingClientRect().width));
    assert.ok(width < 900, `the desktop task detail is no longer a card: ${width}px wide`);
    await context.close();
  }

  console.log("phone-route probe: all assertions passed");
} finally {
  await browser?.close();
  server.close();
}
