// Exercises management layouts and the production gesture binder in a design preview.
// Credential/server behavior is covered separately with real-WebCrypto controller tests.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, stat, mkdir } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { browser as connectBrowser } from "./browser.mjs";
import { SECURITY_PREVIEW_STATES } from "../src/preview/security-states.ts";

const dist = resolve(import.meta.dirname, "../dist");
const artifacts = resolve(import.meta.dirname, "../../.visual-diff/security-management-probe");
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
  for (const state of SECURITY_PREVIEW_STATES.filter(state => state.params.security.startsWith("6") || state.params.security === "7b")) {
    await page.setViewportSize(state.viewport);
    await page.goto(`${origin}/preview.html?${new URLSearchParams(state.params)}`, { waitUntil: "networkidle" });
    await page.locator(state.ready).waitFor();
    assert.equal(await page.locator("h1").count(), 1, state.name);
    assert.equal(await page.locator('x-import, [class*="bp-"]').count(), 0);
    const geometry = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: innerWidth,
      fields: [...document.querySelectorAll("input:not([hidden]), textarea")].map(field => ({ name: field.name, labels: field.labels?.length ?? 0 })),
      buttons: [...document.querySelectorAll("button")].filter(button => button.getBoundingClientRect().width > 0).map(button => ({ text: button.textContent, height: button.getBoundingClientRect().height })),
    }));
    assert.ok(geometry.width <= geometry.viewport, `${state.name}: horizontal overflow`);
    for (const field of geometry.fields) assert.ok(field.labels > 0, `${state.name}: ${field.name} has a label`);
    if (state.viewport.width < 600) for (const button of geometry.buttons) assert.ok(button.height >= 44, `${state.name}: ${button.text} touch target`);
    if (state.name === "security-6d") {
      assert.equal(await page.locator('[data-method-action="confirm-remove"]').count(), 0);
      await page.getByRole("button", { name: "Add another method", exact: true }).click();
      assert.equal(await page.getByRole("heading", { name: "Add an unlock method" }).count(), 1);
    }
  }
  await page.setViewportSize({ width: 1120, height: 720 });
  await page.goto(`${origin}/preview.html?security=6a`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "Manage Encryption password", exact: true }).click();
  await page.getByRole("button", { name: "Change encryption password", exact: true }).click();
  await page.getByLabel("Current password", { exact: true }).fill("public fixture original");
  await page.getByLabel("New password", { exact: true }).fill("short");
  await page.getByRole("button", { name: "Change and check it", exact: true }).click();
  assert.match(await page.getByRole("alert").innerText(), /at least 15 characters/);
  assert.equal(await page.locator('[data-method-management="ready"]').count(), 0);
  await page.getByLabel("Current password", { exact: true }).fill("public fixture original");
  await page.getByLabel("New password", { exact: true }).fill("public fixture replacement");
  await page.getByRole("button", { name: "Change and check it", exact: true }).click();
  assert.match(await page.getByRole("status").innerText(), /Design preview/);
  assert.equal(await page.locator('[data-method-management="ready"]').count(), 0, "a click alone cannot claim a tested method");
  await page.goto(`${origin}/preview.html?security=6m`, { waitUntil: "networkidle" });
  await page.setViewportSize({ width: 393, height: 852 });
  await page.getByRole("button", { name: /Phone passkey.*Checked 3 hours ago/ }).click();
  await page.getByRole("button", { name: "Test this method", exact: true }).click();
  assert.equal(await page.getByRole("button", { name: "Verify with passkey", exact: true }).count(), 1);
  assert.match(await page.locator("main").innerText(), /Verify an active method/);
  await page.getByRole("button", { name: "Cancel", exact: true }).focus();
  await page.keyboard.press("Tab");
  assert.equal(await page.evaluate(() => document.activeElement?.getAttribute("aria-label")), "Close");
  await page.keyboard.press("Shift+Tab");
  assert.equal(await page.evaluate(() => document.activeElement?.textContent), "Cancel");
  await page.keyboard.press("Escape");
  assert.equal(await page.locator('[role="dialog"]').count(), 0);
  await page.goto(`${origin}/preview.html?security=7b`, { waitUntil: "networkidle" });
  assert.match(await page.getByRole("region", { name: "Recovery follow-up" }).innerText(), /3 everyday methods can unlock atlas-7/);
  await page.getByRole("button", { name: "Review them", exact: true }).click();
  assert.equal(await page.locator('[data-method-management="list"]').count(), 1);
  assert.deepEqual(errors, []);
  await context.close();
  console.log("PASS: seven management reference states, desktop/mobile method navigation, last-method refusal, password validation and no simulated credential success.");
} finally {
  await browser?.close();
  await new Promise(resolveClose => server.close(resolveClose));
}
