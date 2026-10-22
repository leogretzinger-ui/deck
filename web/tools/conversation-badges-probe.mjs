import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdir, readFile } from "node:fs/promises";
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
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto(`${origin}/preview.html?conversation-badges`, { waitUntil: "networkidle" });
  await page.waitForSelector('[data-conversation-badge="input"]');
  assert.equal(await page.locator('[data-conversation-badge="approval"]').count(), 1);
  assert.equal(await page.locator('[data-conversation-badge="completed"]').count(), 1);
  assert.equal(await page.getByRole("img", { name: "Agent requests input", exact: true }).count(), 1);
  const assets = resolve(dist, "../../.deck/assets");
  await mkdir(assets, { recursive: true });
  await page.screenshot({ path: join(assets, "conversation-badges-desktop.png") });
  await page.evaluate(() => Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" }));
  await page.locator('[data-session="sess-9"]').click();
  assert.equal(await page.locator('[data-conversation-badge="completed"]').count(), 1);
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    const files = document.querySelector('[data-region="files"]');
    files.dataset.open = "true"; files.dataset.expanded = "true";
    document.dispatchEvent(new Event("visibilitychange"));
  });
  assert.equal(await page.locator('[data-conversation-badge="completed"]').count(), 1, "covering file viewer must not acknowledge");
  await page.evaluate(() => {
    const files = document.querySelector('[data-region="files"]');
    files.dataset.open = "false"; files.dataset.expanded = "false";
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await eventually(() => page.locator('[data-conversation-badge="completed"]').count(), count => count === 0);
  await page.reload({ waitUntil: "networkidle" });
  assert.equal(await page.locator('[data-conversation-badge="completed"]').count(), 0);
  await page.locator('[data-session="sess-1"]').click();
  assert.equal(await page.locator('[data-conversation-badge="approval"]').count(), 1);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.reload({ waitUntil: "networkidle" });
  await page.getByRole("button", { name: "Sessions", exact: true }).click();
  await page.locator('[data-conversation-badge="input"]').waitFor({ state: "visible" });
  await page.screenshot({ path: join(assets, "conversation-badges-mobile.png") });
  assert.equal(await page.locator('[data-conversation-badge="input"]').count(), 1);
  assert.equal(await page.locator('[data-conversation-badge="approval"]').count(), 1);
  assert.deepEqual(errors, []);
  console.log("PASS synthetic conversation badges: accessible states, hidden selection, visible read, reload persistence, pending approval, mobile");
  await context.close();
} finally {
  await browser?.close();
  await new Promise(done => server.close(done));
}
