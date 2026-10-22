// Exercises the shared renderer through the production action binder. The
// preview does not perform enrollment or claim storage/runtime verification.
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, stat, mkdir } from "node:fs/promises";
import { extname, resolve, sep } from "node:path";
import { browser as connectBrowser } from "./browser.mjs";
import { SECURITY_PREVIEW_STATES } from "../src/preview/security-states.ts";

const dist = resolve(import.meta.dirname, "../dist");
const artifacts = resolve(import.meta.dirname, "../../.visual-diff/security-enrollment-probe");
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
  for (const state of SECURITY_PREVIEW_STATES.filter(state => /^security-[123]/.test(state.name))) {
    await page.setViewportSize(state.viewport);
    await page.goto(`${origin}/preview.html?${new URLSearchParams(state.params)}`, { waitUntil: "networkidle" });
    await page.locator(state.ready).waitFor();
    await page.evaluate(() => document.fonts.ready);
    assert.equal(await page.locator("[data-preview-error]").count(), 0, state.name);
    assert.equal(await page.locator("h1").count(), 1, `${state.name}: one page heading`);
    assert.equal(await page.locator('x-import, [class*="bp-"]').count(), 0, `${state.name}: native components`);
    const geometry = await page.evaluate(() => ({
      width: document.documentElement.scrollWidth,
      viewport: innerWidth,
      headingCaps: getComputedStyle(document.querySelector("h1")).fontVariantCaps,
      buttons: [...document.querySelectorAll("button")].filter(button => button.getBoundingClientRect().width > 0).map(button => ({ label: button.textContent.trim(), height: button.getBoundingClientRect().height })),
      fields: [...document.querySelectorAll("input:not([hidden]), textarea")].map(field => ({ name: field.name, labels: field.labels?.length ?? 0 })),
    }));
    assert.ok(geometry.width <= geometry.viewport, `${state.name}: horizontal overflow`);
    assert.equal(geometry.headingCaps, "normal", `${state.name}: headings preserve sentence case`);
    if (state.viewport.width < 600) for (const button of geometry.buttons) assert.ok(button.height >= 44, `${state.name}: ${button.label} has a 44px touch target`);
    for (const field of geometry.fields) assert.ok(field.labels > 0, `${state.name}: ${field.name} has a label`);
    if (state.name === "security-2b") {
      assert.equal(await page.getByText("Waiting for your browser").count(), 1);
      assert.equal(await page.getByText("Choose where to save it").count(), 0, "Deck must not imitate a native credential picker");
    }
  }
  await page.setViewportSize({ width: 1120, height: 900 });
  await page.goto(`${origin}/preview.html?security=1a`, { waitUntil: "networkidle" });
  await page.getByRole("button", { name: "What this protects" }).click();
  assert.equal(await page.getByRole("button", { name: "What this protects" }).getAttribute("aria-expanded"), "true");
  assert.match(await page.locator("#enroll-protection").innerText(), /Someone who controls the machine could deliberately look/);
  await page.getByRole("button", { name: "Sign in with Google to continue" }).click();
  await page.getByRole("button", { name: "Choose a passkey" }).click();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("button", { name: "Use a password instead" }).click();
  await page.getByLabel("Encryption password", { exact: true }).fill("too short");
  await page.getByLabel("Repeat it").fill("too short");
  await page.getByRole("button", { name: "Set password and check it" }).click();
  assert.match(await page.getByRole("alert").innerText(), /at least 15 characters/);
  await page.getByLabel("Encryption password", { exact: true }).fill("correct horse cloud river");
  await page.getByLabel("Repeat it").fill("different horse cloud river");
  await page.getByRole("button", { name: "Set password and check it" }).click();
  assert.match(await page.getByRole("alert").innerText(), /passwords do not match/);
  assert.equal(await page.locator('[data-enrollment="method-ready"]').count(), 0);
  await page.goto(`${origin}/preview.html?security=3n`, { waitUntil: "networkidle" });
  const check = page.getByRole("button", { name: "Check key", exact: true });
  assert.equal(await check.isDisabled(), true);
  await page.getByLabel("Recovery key", { exact: true }).fill("DK1-example");
  assert.equal(await check.isEnabled(), true);
  await page.getByLabel("Recovery key", { exact: true }).fill(" ");
  assert.equal(await check.isDisabled(), true);
  await page.goto(`${origin}/preview.html?security=3d`, { waitUntil: "networkidle" });
  const recovery = page.getByLabel("Recovery key", { exact: true });
  await recovery.fill("DK1 ABCD EFGH");
  await recovery.blur();
  assert.equal(await recovery.inputValue(), "DK1 ABCD EFGH");
  assert.deepEqual(await page.locator(".enroll-group-preview > span").allTextContents(), ["DK1", "ABCD", "EFGH"]);
  assert.deepEqual(errors, []);
  await context.close();
  console.log(`PASS: ${SECURITY_PREVIEW_STATES.filter(state => /^security-[123]/.test(state.name)).length} enrollment render states, disclosure, browser-owned picker, password validation and recovery input. Production enrollment/runtime not exercised.`);
} finally {
  await browser?.close();
  await new Promise(resolveClose => server.close(resolveClose));
}
