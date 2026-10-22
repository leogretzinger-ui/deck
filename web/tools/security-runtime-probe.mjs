// Exercises the shared renderer through the production action binder. The
// preview does not perform enrollment or claim storage/runtime verification.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, stat, mkdir } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { browser as connectBrowser } from "./browser.mjs";
import { RUNTIME_PREVIEW_FIGURES } from "../src/preview/security-runtime-preview.ts";
import { SECURITY_PREVIEW_STATES } from "../src/preview/security-states.ts";

const dist = resolve(import.meta.dirname, "../dist");
const artifacts = resolve(import.meta.dirname, "../../.visual-diff/security-runtime-probe");
await mkdir(artifacts, { recursive: true });
const mime = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".woff2": "font/woff2", ".png": "image/png" };
const server = createServer(async (request, response) => {
  try {
    const file = resolve(dist, `.${new URL(request.url, "http://local").pathname}`);
    if (!file.startsWith(`${dist}${sep}`) || !(await stat(file)).isFile()) throw new Error("not found");
    response.writeHead(200, { "Content-Type": mime[extname(file)] ?? "application/octet-stream", "Cache-Control": "no-store" });
    response.end(await readFile(file));
  } catch { response.writeHead(404); response.end("not found"); }
});
await new Promise(resolveListen => server.listen(0, "127.0.0.1", resolveListen));
const origin = `http://127.0.0.1:${server.address().port}`;
let browser;
const errors = [];
try {
  ({ browser } = await connectBrowser());
  const context = await browser.newContext({ reducedMotion: "reduce", colorScheme: "dark" });
  await context.route("**/*", route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
  const page = await context.newPage();
  page.on("pageerror", error => errors.push(error.message));
  for (const state of SECURITY_PREVIEW_STATES.filter(state => RUNTIME_PREVIEW_FIGURES.includes(state.params.security))) {
    await page.setViewportSize(state.viewport);
    await page.goto(`${origin}/preview.html?${new URLSearchParams(state.params)}`, { waitUntil: "networkidle" });
    await page.locator(state.ready).waitFor();
    await page.evaluate(() => document.fonts.ready);
    assert.equal(await page.locator("[data-preview-error]").count(), 0, state.name);
    assert.equal(await page.locator("h1:visible").count(), state.name === "security-4a" ? 0 : 1, `${state.name}: one page heading`);
    assert.equal(await page.locator('x-import, [class*="bp-"]').count(), 0, `${state.name}: native components`);
    const geometry = await page.evaluate(() => ({
      width: document.documentElement.scrollWidth,
      viewport: innerWidth,
      headingCaps: document.querySelector("h1") ? getComputedStyle(document.querySelector("h1")).fontVariantCaps : "normal",
      buttons: [...document.querySelectorAll("button")].filter(button => button.getBoundingClientRect().width > 0).map(button => ({ label: button.textContent.trim(), height: Math.max(button.getBoundingClientRect().height, parseFloat(getComputedStyle(button, "::after").height) || 0) })),
      fields: [...document.querySelectorAll("input:not([hidden]), textarea")].map(field => ({ name: field.name, labels: field.labels?.length ?? 0 })),
    }));
    assert.ok(geometry.width <= geometry.viewport, `${state.name}: horizontal overflow`);
    assert.equal(geometry.headingCaps, "normal", `${state.name}: headings preserve sentence case`);
    if (state.viewport.width < 600) for (const button of geometry.buttons) assert.ok(button.height >= 44, `${state.name}: ${button.label} has a 44px touch target`);
    for (const field of geometry.fields) assert.ok(field.labels > 0, `${state.name}: ${field.name} has a label`);
    if (state.name === "security-4k" || state.name === "security-4l") {
      assert.equal(await page.getByRole("alert").count(), state.name === "security-4l" ? 1 : 0);
      assert.equal(await page.getByRole("button", { name: state.name === "security-4l" ? "Try again" : "Unlock", exact: true }).count(), 1);
    }
  }
  await page.setViewportSize({ width: 1120, height: 720 });
  await page.goto(`${origin}/preview.html?security=7a`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "Need help getting back in?" }).click();
  await page.getByRole("heading", { name: "Who can help with what" }).waitFor();
  await page.getByRole("button", { name: "No unlock methods left?" }).click();
  await page.getByRole("heading", { name: "Without an unlock method, atlas-7 cannot be opened" }).waitFor();
  assert.equal(await page.getByRole("button", { name: "Start a new workspace" }).count(), 0);
  await page.getByRole("button", { name: "Get help", exact: true }).click();
  await page.getByRole("heading", { name: "Who can help with what" }).waitFor();
  assert.deepEqual(errors, []);
  await context.close();
  console.log("PASS:23 runtime/recovery preview states, native markup, responsive geometry and real help navigation. No native runtime claim.");
} finally {
  await browser?.close();
  await new Promise(resolveClose => server.close(resolveClose));
}
