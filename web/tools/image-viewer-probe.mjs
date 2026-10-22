// Deterministic browser probe for the Files panel's image viewer.
//
// It drives the fixture preview (preview.html) of the BUILT page, where the
// fixture's image route is a set of bundled images, and answers what only a
// real engine can: that Fit never scales an image past the canvas or past
// its natural size, that 100% scrolls, that the checkerboard is drawn only
// behind transparency, which requests each state makes — none past the
// size cap — that replacing the open image shows the new one, and the
// phone's pushed screen. The removed-file state is also driven under the
// Vite dev server, whose fallback answers 200 for any path.
//
// Usage: pnpm build && node tools/image-viewer-probe.mjs
// Requires: dist/ built, and a browser — the agent-browser sidecar via
// NOISE_BROWSER_WS, or a local one (see tools/browser.mjs).
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import net from "node:net";
import { readFile } from "node:fs/promises";
import { existsSync } from "node:fs";
import { extname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

if (process.env.PLAYWRIGHT_BROWSERS_PATH === undefined &&
    existsSync("/workspace/tools/ms-playwright")) {
  process.env.PLAYWRIGHT_BROWSERS_PATH = "/workspace/tools/ms-playwright";
}
const { browser: getBrowser } = await import("./browser.mjs");

const WEB = resolve(import.meta.dirname, "..");
const DIST = join(WEB, "dist");
const MIME = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".map": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".webmanifest": "application/manifest+json",
  ".woff2": "font/woff2",
};

if (!existsSync(join(DIST, "preview.html"))) {
  throw new Error("dist/preview.html is missing — run `pnpm build` first");
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://x");
  const file = join(DIST, decodeURIComponent(url.pathname));
  if (!file.startsWith(DIST) || !existsSync(file)) {
    res.writeHead(404);
    res.end("not found");
    return;
  }
  res.writeHead(200, { "Content-Type": MIME[extname(file)] ?? "application/octet-stream" });
  res.end(req.method === "HEAD" ? undefined : await readFile(file));
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const origin = `http://127.0.0.1:${server.address().port}`;

let browser;
try {
  const { browser: got, via } = await getBrowser();
  browser = got;
  console.log(`browser: ${via}`);

  // open loads a preview state and records every image the page asks for.
  // routes lets a check answer one URL pattern itself.
  async function open(query, { width = 1180, height = 720, routes = [], base = origin } = {}) {
    const context = await browser.newContext({ viewport: { width, height } });
    const page = await context.newPage();
    // Every source any <img> is given, blob: and data: included, which
    // never reach the route layer.
    await page.addInitScript(() => {
      window.__imageSources = [];
      const descriptor = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, "src");
      Object.defineProperty(HTMLImageElement.prototype, "src", {
        ...descriptor,
        set(value) {
          window.__imageSources.push(String(value));
          descriptor.set.call(this, value);
        },
      });
    });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const images = [];
    await page.route("**/*", (route) => {
      const request = route.request();
      if (request.resourceType() === "image" || request.method() === "HEAD") {
        images.push(`${request.method()} ${new URL(request.url()).pathname}`);
      }
      for (const [pattern, answer] of routes) {
        if (pattern.test(request.url())) return answer(route);
      }
      return route.continue();
    });
    await page.goto(`${base}/preview.html?${query}`, { waitUntil: "networkidle" });
    await page.waitForFunction(() =>
      document.documentElement.dataset.previewState !== undefined ||
      document.documentElement.dataset.previewError !== undefined);
    const reached = await page.evaluate(() => document.documentElement.dataset);
    assert.equal(reached.previewError, undefined, `${query}: ${reached.previewError}`);
    assert.deepEqual(errors, [], `${query}: page errors`);
    const panel = page.locator('[data-region="files"]');
    return { context, page, panel, images, state: reached.previewState };
  }

  const box = (locator) => locator.boundingBox().then((b) => {
    assert.ok(b !== null, "element has a layout box");
    return b;
  });
  const status = (panel) => panel.locator("[data-files-viewer-status]").innerText();

  // ---- Fit: the whole image inside the canvas, at the image's own ratio ----
  {
    const { context, panel, images, state } = await open("panel=files&viewer=image");
    assert.equal(state, "viewer-image");
    const canvas = await box(panel.locator('[data-files-image-canvas="fit"]'));
    const frame = await box(panel.locator("[data-files-image-frame]"));
    assert.ok(frame.width <= canvas.width - 40 + 0.5 && frame.height <= canvas.height - 40 + 0.5,
      `the fitted image stays inside the 20px inset: ${JSON.stringify({ canvas, frame })}`);
    assert.ok(Math.abs(frame.width / frame.height - 1440 / 900) < 0.01, "the fit keeps the image's ratio");
    assert.ok(frame.width < 1440, "a large image is scaled down to fit");
    assert.equal(await status(panel), "PNG · 1440 × 900 · 412.0 KB · modified 2m ago",
      "the decode's dimensions join the listing's facts");
    const ground = await panel.locator("[data-files-image-frame]").evaluate((el) => getComputedStyle(el).backgroundImage);
    assert.equal(ground, "none", "an opaque image gets no checkerboard");
    assert.equal(await panel.locator('[data-files-zoom="fit"]').getAttribute("aria-pressed"), "true");
    assert.equal(await panel.locator("[data-files-wrap]").count(), 0, "the zoom pair takes the wrap toggle's slot");
    assert.equal(images.filter((r) => r.includes("/home-")).length, 1, `one request for the image: ${images}`);
    await context.close();
    console.log("image-viewer probe: Fit draws the whole image inside the canvas");
  }

  // ---- Fit never upscales; the checkerboard is only behind alpha ----
  {
    const { context, panel } = await open("panel=files&viewer=image-alpha&expanded=1", { width: 1400, height: 900 });
    const canvas = await box(panel.locator('[data-files-image-canvas="fit"]'));
    const frame = await box(panel.locator("[data-files-image-frame]"));
    assert.ok(canvas.width > 600 && canvas.height > 600, "the expanded canvas is larger than the image");
    assert.equal(Math.round(frame.width), 512, "Fit stops at the natural width");
    assert.equal(Math.round(frame.height), 512, "Fit stops at the natural height");
    assert.equal(await panel.locator("[data-files-image-frame]").getAttribute("data-files-alpha"), "true");
    const ground = await panel.locator("[data-files-image-frame]").evaluate((el) => getComputedStyle(el).backgroundImage);
    assert.match(ground, /conic-gradient/, "a transparent image sits on the checkerboard");
    await context.close();
    console.log("image-viewer probe: Fit never upscales, and alpha earns the checkerboard");
  }

  // ---- 100%: natural size, scrolling both ways; Fit comes back ----
  {
    const { context, panel } = await open("panel=files&viewer=image-actual");
    const frame = await box(panel.locator("[data-files-image-frame]"));
    assert.equal(Math.round(frame.width), 1440);
    assert.equal(Math.round(frame.height), 900);
    const scroller = panel.locator('[data-files-image-canvas="actual"]');
    const overflow = await scroller.evaluate((el) => ({
      x: el.scrollWidth > el.clientWidth, y: el.scrollHeight > el.clientHeight,
      inset: el.scrollWidth - 1440,
    }));
    assert.ok(overflow.x && overflow.y, "100% scrolls on both axes");
    assert.equal(overflow.inset, 40, "the 20px inset holds on both sides of the scrolled image");
    await scroller.evaluate((el) => el.scrollTo(300, 200));
    assert.deepEqual(await scroller.evaluate((el) => [el.scrollLeft, el.scrollTop]), [300, 200]);
    assert.equal(await panel.locator('[data-files-zoom="actual"]').getAttribute("aria-pressed"), "true");
    await panel.locator('[data-files-zoom="fit"]').click();
    await panel.locator('[data-files-image-canvas="fit"] [data-files-image-frame] img').waitFor();
    assert.equal(await panel.locator('[data-files-zoom="fit"]').getAttribute("aria-pressed"), "true");
    await context.close();
    console.log("image-viewer probe: 100% scrolls at natural size");
  }

  // ---- past the cap: refused from the listing, no request at all ----
  {
    const { context, panel, images } = await open("panel=files&viewer=image-oversized");
    await panel.getByText("Too large to preview").waitFor();
    await panel.getByText("Previews stop at 16 MiB. This file is 46 MiB.").waitFor();
    assert.deepEqual(images.filter((r) => r.includes("render-8k")), [], "nothing is fetched past the cap");
    assert.deepEqual(images.filter((r) => /fixture-images|\/assets\/[^/]*\.png/.test(r)), [],
      `no image request of any kind: ${images}`);
    await panel.locator('[data-files-image-message="oversized"] [data-files-download]').waitFor();
    assert.equal(await panel.locator("[data-files-zoom]").count(), 0, "nothing to zoom");
    assert.equal(await status(panel), "PNG · 48.2 MB · modified 3m ago");
    await context.close();
    console.log("image-viewer probe: an oversized image is refused without a request");
  }

  // ---- removed after the listing: no download, Refresh list ----
  {
    const { context, page, panel } = await open("panel=files&viewer=image-gone");
    await panel.getByText("No longer on disk").waitFor();
    await panel.getByText("chart-latency.png was in the list, then removed before Deck could read it.").waitFor();
    assert.equal(await panel.locator("[data-files-download]").count(), 0, "no download anywhere for a removed file");
    const loads = () => page.evaluate(() => window.__imageSources.length);
    const before = await loads();
    assert.ok(before >= 1, "the viewer tried to load the image");
    await panel.getByRole("button", { name: "Refresh list" }).click();
    // The fixture still lists the file and still has no image behind it, so
    // the viewer reads it again and lands in the same honest state.
    await page.waitForFunction((n) => window.__imageSources.length > n, before);
    await panel.getByText("No longer on disk").waitFor();
    await context.close();
    console.log("image-viewer probe: a removed image offers Refresh list and no download");
  }

  // ---- a 415 from the image route is the undecodable state ----
  {
    const refuse = (route) => route.fulfill({
      status: 415, contentType: "application/json",
      body: JSON.stringify({ error: "not_image", message: "file is not an image" }),
    });
    const { context, panel, images } = await open("panel=files&viewer=image-undecodable", {
      routes: [[/capture-partial/, refuse]],
    });
    await panel.getByText("This file didn’t decode as an image.").waitFor();
    await panel.getByText("It may still be being written. Download it to check.").waitFor();
    await panel.locator('[data-files-image-message="undecodable"] [data-files-download]').waitFor();
    assert.ok(images.some((r) => r.startsWith("HEAD ") && r.includes("capture-partial")));
    await context.close();
    console.log("image-viewer probe: a 415 reads as undecodable with its download");
  }

  // ---- replacing the open image: the viewer follows the new file ----
  const fixtureImage = (name) => readFile(fileURLToPath(new URL(`../src/app/fixture-images/${name}`, import.meta.url)));
  async function replaceOpenImage(page, panel, buffer) {
    await panel.locator("[data-files-input]").setInputFiles({ name: "home.png", mimeType: "image/png", buffer });
    await page.locator("[data-files-conflict-replace]").click();
    await page.locator("[data-files-conflict]").waitFor({ state: "detached" });
  }
  {
    const { context, page, panel } = await open("panel=files&viewer=image");
    const img = panel.locator("[data-files-image-frame] img");
    const before = await img.getAttribute("src");
    assert.equal(await status(panel), "PNG · 1440 × 900 · 412.0 KB · modified 2m ago");
    await replaceOpenImage(page, panel, await fixtureImage("home-mobile.png"));
    await page.waitForFunction(() =>
      document.querySelector("[data-files-viewer-status]")?.textContent?.includes("393 × 852"));
    const after = await img.getAttribute("src");
    assert.notEqual(after, before, "the replaced file is loaded under a new source");
    assert.ok(after.startsWith("blob:"), `the viewer shows the stored bytes: ${after}`);
    assert.match(await status(panel), /^PNG · 393 × 852 · 3\.1 KB · modified < 1m ago$/,
      "the footer's facts and the pixels are the same generation");
    const frame = await box(panel.locator("[data-files-image-frame]"));
    assert.ok(Math.abs(frame.width / frame.height - 393 / 852) < 0.01, "the frame takes the new image's ratio");
    await context.close();
    console.log("image-viewer probe: replacing the open image shows the new one");
  }
  {
    const { context, page, panel } = await open("panel=files&viewer=image");
    await page.evaluate(() => { window.__imageSources = []; });
    await replaceOpenImage(page, panel, Buffer.alloc((16 << 20) + 1));
    await panel.getByText("Too large to preview").waitFor();
    await panel.getByText("Previews stop at 16 MiB. This file is 16.1 MiB.").waitFor();
    assert.deepEqual(await page.evaluate(() => window.__imageSources), [],
      "a replacement past the cap is never requested");
    assert.equal(await panel.locator("img").count(), 0);
    await context.close();
    console.log("image-viewer probe: a replacement past the cap is refused without a request");
  }

  // ---- a removed image under the Vite dev server ----
  {
    const port = await new Promise((resolvePort, reject) => {
      const probe = net.createServer();
      probe.once("error", reject);
      probe.listen(0, "127.0.0.1", () => {
        const { port: free } = probe.address();
        probe.close(() => resolvePort(free));
      });
    });
    const devOrigin = `http://127.0.0.1:${port}`;
    let output = "";
    const vite = spawn(process.env.PNPM_BIN ?? "pnpm",
      ["exec", "vite", "--host", "127.0.0.1", "--port", String(port), "--strictPort"],
      { cwd: WEB, stdio: ["ignore", "pipe", "pipe"] });
    vite.stdout.on("data", (chunk) => { output += chunk; });
    vite.stderr.on("data", (chunk) => { output += chunk; });
    try {
      for (let attempt = 0; ; attempt++) {
        try {
          if ((await fetch(devOrigin)).ok) break;
        } catch {}
        if (attempt === 200) throw new Error(`Vite did not start:\n${output}`);
        await new Promise((r) => setTimeout(r, 50));
      }
      const fallback = await fetch(`${devOrigin}/fixture-images/missing`, { method: "HEAD" });
      assert.equal(fallback.status, 200, "the dev server answers an unknown path, so it cannot be what says 404");
      const { context, panel, state } = await open("panel=files&viewer=image-gone", { base: devOrigin });
      assert.equal(state, "viewer-image-gone");
      await panel.getByText("No longer on disk").waitFor();
      assert.equal(await panel.locator("[data-files-download]").count(), 0);
      await context.close();
    } finally {
      vite.kill();
    }
    console.log("image-viewer probe: a removed image reads as gone under the Vite dev server");
  }

  // ---- phone: the pushed screen, every fact, touch-height zoom ----
  {
    const { context, page, panel } = await open("panel=files&viewer=image", { width: 393, height: 852 });
    await panel.locator("[data-files-back]").waitFor();
    assert.equal(await panel.locator("[data-files-list]").isVisible(), false, "the list leaves for the pushed screen");
    assert.equal(await panel.locator("[data-files-expand]").isVisible(), false, "no expand on a phone");
    assert.ok(await panel.locator("[data-files-viewer-header] [data-files-download]").isVisible());
    for (const zoom of ["fit", "actual"]) {
      const b = await box(panel.locator(`[data-files-zoom="${zoom}"]`));
      assert.equal(Math.round(b.height), 44, `${zoom} is a 44px touch target`);
    }
    assert.equal(await status(panel), "PNG · 1440 × 900 · 412.0 KB · modified 2m ago",
      "the phone keeps every image fact");
    await panel.locator("[data-files-back]").click();
    await panel.locator("[data-files-list]").waitFor();
    assert.equal(await page.locator('[data-region="files"]').getAttribute("data-viewing"), "false");
    await context.close();
    console.log("image-viewer probe: the phone viewer is the pushed screen");
  }

  console.log("image-viewer probe: all assertions passed");
} finally {
  await browser?.close();
  await new Promise((r) => server.close(r));
}
